package api

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"

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
