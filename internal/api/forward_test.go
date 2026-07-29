package api

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dynaum/kubeside/internal/timeline"
	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// A port-forward is a write. It is the one read-shaped action that opens a
// socket into a cluster, and for a while it was the only action that asked
// neither the cluster nor the guard whether it could.
//
// Both refusals have to happen on the server. The browser disables the button
// when the permission is missing, but a control that is only disabled in the
// UI is not a control at all: anything holding the session token can post the
// request the button would have sent.

// reviews makes every SelfSubjectAccessReview answer the same way, and counts
// them. The count is the evidence that the question was asked at all: an
// allow-path test that only checks the absence of a refusal would pass just as
// well against a server that never checked.
func reviews(client *fake.Clientset, allowed bool) *int32 {
	var asked int32
	client.PrependReactor("create", "selfsubjectaccessreviews", func(a ktesting.Action) (bool, runtime.Object, error) {
		atomic.AddInt32(&asked, 1)
		review := a.(ktesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
		review.Status.Allowed = allowed
		if !allowed {
			review.Status.Reason = "no RoleBinding grants it"
		}
		return true, review, nil
	})
	return &asked
}

func TestForwardIsRefusedWhenTheClusterRefusesPortforward(t *testing.T) {
	client := degradedCluster()
	asked := reviews(client, false)
	svc := degradedService(t, client, nil)

	_, err := svc.StartForward(ForwardRequest{
		Context: "qa1", Namespace: "team-a", Workload: "checkout", RemotePort: 8080,
	})

	var forbidden *ForbiddenError
	if !errors.As(err, &forbidden) {
		t.Fatalf("err = %v, want a refusal from the cluster's own answer", err)
	}
	// The verb is named, because "denied" without a verb is not something a
	// developer can act on.
	if !strings.Contains(err.Error(), "portforward") {
		t.Errorf("err = %q, want the verb named", err)
	}
	if atomic.LoadInt32(asked) == 0 {
		t.Error("the cluster was never asked whether this was allowed")
	}
}

// prod1 classifies as production, whose default write policy is deny. A tunnel
// into it is a write in an environment that refuses writes, and the guard has
// to say so even when the cluster would have allowed it.
func TestForwardIsRefusedByTheEnvironmentWritePolicy(t *testing.T) {
	client := degradedCluster()
	reviews(client, true)
	svc := degradedService(t, client, nil)

	_, err := svc.StartForward(ForwardRequest{
		Context: "prod1", Namespace: "team-a", Workload: "checkout", RemotePort: 8080,
	})

	var forbidden *ForbiddenError
	if !errors.As(err, &forbidden) {
		t.Fatalf("err = %v, want the write policy to refuse", err)
	}
	if !strings.Contains(err.Error(), "denied") && !strings.Contains(err.Error(), "policy") {
		t.Errorf("err = %q, want the refusal to name the policy", err)
	}
}

// The pod is what the tunnel actually reaches, so naming one outside the
// workload is how a caller would forward to something they never opened. The
// workload field is not a label; it is the scope.
func TestForwardRefusesAPodOutsideTheNamedWorkload(t *testing.T) {
	client := degradedCluster()
	reviews(client, true)
	svc := degradedService(t, client, nil)

	_, err := svc.StartForward(ForwardRequest{
		Context: "qa1", Namespace: "team-a", Workload: "checkout",
		Pod: "etcd-ip-10-0-1-4", RemotePort: 2379,
	})
	if err == nil {
		t.Fatal("a pod outside the workload was forwarded to")
	}
	if !strings.Contains(err.Error(), "etcd-ip-10-0-1-4") {
		t.Errorf("err = %q, want the refused pod named", err)
	}
}

// The permission and the policy both pass here, so the refusals above are
// about the gates rather than about the request being malformed. The dial
// itself fails in a test, which is fine: what matters is that it was reached.
func TestForwardWithBothGatesPassedReachesTheDial(t *testing.T) {
	client := degradedCluster()
	asked := reviews(client, true)
	svc := degradedService(t, client, nil)

	_, err := svc.StartForward(ForwardRequest{
		Context: "qa1", Namespace: "team-a", Workload: "checkout",
		Pod: "checkout-1", RemotePort: 8080,
	})

	var forbidden *ForbiddenError
	if errors.As(err, &forbidden) {
		t.Fatalf("err = %v; both gates allowed this and it was still refused", err)
	}
	if atomic.LoadInt32(asked) == 0 {
		t.Fatal("the tunnel was opened without the cluster ever being asked")
	}
}

// Arming an environment is the most consequential thing a developer can do in
// this product without touching a workload, and it was filed under a key
// nothing reads.
//
// The record was keyed by context, namespace, and workload. A bare unlock
// carries none of the last two — the dialog sends them empty, because arming an
// environment is not about one app — so the entry landed under "qa1|/" while
// every timeline reads "qa1|team-a/checkout". It was written and then lost.
func TestAnUnlockIsOnTheTimelineOfEveryAppInThatEnvironment(t *testing.T) {
	client := degradedCluster()
	reviews(client, true)
	svc := degradedService(t, client, nil)

	if _, err := svc.Gate(GateRequest{Context: "qa1", Unlock: "paging on-call for a stuck rollout"}); err != nil {
		t.Fatalf("Gate: %v", err)
	}

	view, err := svc.Timeline("qa1", "team-a", "checkout")
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}

	var found bool
	for _, e := range view.Entries {
		if e.Kind == timeline.KindBreakGlass {
			found = true
			if !strings.Contains(e.Detail, "paging on-call") {
				t.Errorf("detail = %q, want the stated reason kept", e.Detail)
			}
		}
	}
	if !found {
		t.Fatal("the unlock was recorded where nothing can read it")
	}
}

// The window is per context, so the record is too. An unlock in one environment
// must not appear on another's timeline, which would read as production having
// been armed when it was not.
func TestAnUnlockDoesNotAppearInAnotherEnvironment(t *testing.T) {
	client := degradedCluster()
	reviews(client, true)
	svc := degradedService(t, client, nil)

	if _, err := svc.Gate(GateRequest{Context: "qa1", Unlock: "a reason"}); err != nil {
		t.Fatalf("Gate: %v", err)
	}

	view, err := svc.Timeline("prod1", "team-a", "checkout")
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	for _, e := range view.Entries {
		if e.Kind == timeline.KindBreakGlass {
			t.Fatal("an unlock in qa showed up on prod's timeline")
		}
	}
}

// The reveal record is the audit trail for a secret leaving the cluster, and
// the workload it was filed under is caller-supplied. An empty one dropped the
// record entirely rather than filing it somewhere readable.
func TestARevealIsRecordedEvenWithoutAWorkload(t *testing.T) {
	client := degradedCluster()
	reviews(client, true)
	svc := degradedService(t, client, nil)

	if _, err := svc.RevealSecret("qa1", "team-a", "db", "PASSWORD", ""); err != nil {
		t.Fatalf("RevealSecret: %v", err)
	}

	view, err := svc.Timeline("qa1", "team-a", "checkout")
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	for _, e := range view.Entries {
		if e.Kind == timeline.KindReveal {
			return
		}
	}
	t.Fatal("a reveal with no workload left no record")
}

// Exec has the same shape as a forward: the pod is the scope, not a label. A
// namespace-scoped permission covers every team sharing that namespace, so
// without this check a caller reaches pods they never opened, and the record
// lands under whatever workload they claimed.
func TestExecRefusesAPodOutsideTheNamedWorkload(t *testing.T) {
	client := degradedCluster()
	reviews(client, true)
	svc := degradedService(t, client, nil)

	_, _, err := svc.StartExec(context.Background(), ExecRequest{
		Context: "qa1", Namespace: "team-a", Workload: "checkout",
		Pod: "someone-elses-pod", Container: "app",
	}, func([]byte) {})
	if err == nil {
		t.Fatal("a pod outside the workload was exec'd into")
	}
	if !strings.Contains(err.Error(), "someone-elses-pod") {
		t.Errorf("err = %q, want the refused pod named", err)
	}
}

// The workload on the audit entry is the one the pod resolves into, not a
// string the caller supplied. With the scope check above in place a mismatched
// workload is refused outright, so the entry can only ever name the workload
// the pod is really in.
func TestAnExecIsRecordedUnderTheWorkloadThePodBelongsTo(t *testing.T) {
	client := degradedCluster()
	reviews(client, true)
	svc := degradedService(t, client, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The session cannot dial in a test. The record is written when the session
	// is accepted, not when the shell exits.
	_, _, _ = svc.StartExec(ctx, ExecRequest{
		Context: "qa1", Namespace: "team-a", Workload: "checkout",
		Pod: "checkout-1", Container: "app",
	}, func([]byte) {})

	view, err := svc.Timeline("qa1", "team-a", "checkout")
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	for _, e := range view.Entries {
		if e.Kind == timeline.KindExec {
			if !strings.Contains(e.Title, "checkout-1") {
				t.Errorf("title = %q, want the pod named", e.Title)
			}
			return
		}
	}
	t.Fatal("the exec left no record on the workload it opened in")
}

// The typed name was a field the server emitted and never read. The dialog
// asked for it, the browser rendered it, and anything calling the API directly
// skipped it — which made it a decoration rather than a control, in exactly the
// environments a developer asked to be slowed down in.
//
// stg1 classifies as staging, whose default write policy is confirm.
func TestExecInAConfirmEnvironmentNeedsTheNameTyped(t *testing.T) {
	client := degradedCluster()
	reviews(client, true)
	svc := degradedService(t, client, nil)

	_, _, err := svc.StartExec(context.Background(), ExecRequest{
		Context: "stg1", Namespace: "team-a", Workload: "checkout",
		Pod: "checkout-1", Container: "app",
	}, func([]byte) {})

	var forbidden *ForbiddenError
	if !errors.As(err, &forbidden) {
		t.Fatalf("err = %v, want the missing confirmation to refuse it", err)
	}
	// The refusal says what to type, because "confirmation required" without
	// the word is not something anybody can act on.
	// The workload is what has to be typed: a pod name is a hash nobody chose.
	if !strings.Contains(err.Error(), `"checkout"`) {
		t.Errorf("err = %q, want the expected confirmation named", err)
	}
}

func TestExecRefusesAConfirmationThatDoesNotMatch(t *testing.T) {
	client := degradedCluster()
	reviews(client, true)
	svc := degradedService(t, client, nil)

	_, _, err := svc.StartExec(context.Background(), ExecRequest{
		Context: "stg1", Namespace: "team-a", Workload: "checkout",
		Pod: "checkout-1", Container: "app", Confirm: "checkout-1",
	}, func([]byte) {})

	var forbidden *ForbiddenError
	if !errors.As(err, &forbidden) {
		t.Fatalf("err = %v; a near-miss confirmation was accepted", err)
	}
}

func TestExecProceedsWhenTheNameMatches(t *testing.T) {
	client := degradedCluster()
	reviews(client, true)
	svc := degradedService(t, client, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, _, err := svc.StartExec(ctx, ExecRequest{
		Context: "stg1", Namespace: "team-a", Workload: "checkout",
		Pod: "checkout-1", Container: "app", Confirm: "checkout",
	}, func([]byte) {})

	var forbidden *ForbiddenError
	if errors.As(err, &forbidden) {
		t.Fatalf("err = %v; the name was typed correctly and it was still refused", err)
	}
}

// A tunnel into staging is a write too, and it goes through the same guard, so
// it asks for the same ceremony.
func TestForwardInAConfirmEnvironmentNeedsTheNameTyped(t *testing.T) {
	client := degradedCluster()
	reviews(client, true)
	svc := degradedService(t, client, nil)

	_, err := svc.StartForward(ForwardRequest{
		Context: "stg1", Namespace: "team-a", Workload: "checkout", RemotePort: 8080,
	})

	var forbidden *ForbiddenError
	if !errors.As(err, &forbidden) {
		t.Fatalf("err = %v, want the missing confirmation to refuse it", err)
	}
}

// qa asks for nothing, and adding a dialog to the environment somebody works in
// all day would make the ceremony meaningless everywhere else.
func TestNothingIsTypedInAnAllowEnvironment(t *testing.T) {
	client := degradedCluster()
	reviews(client, true)
	svc := degradedService(t, client, nil)

	_, err := svc.StartForward(ForwardRequest{
		Context: "qa1", Namespace: "team-a", Workload: "checkout", RemotePort: 8080,
	})

	var forbidden *ForbiddenError
	if errors.As(err, &forbidden) {
		t.Fatalf("err = %v; qa asked for a confirmation it should not want", err)
	}
}
