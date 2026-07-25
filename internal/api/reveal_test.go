package api

import (
	"errors"
	"strings"
	"testing"

	"time"

	"github.com/dynaum/kubeside/internal/clusters"
	"github.com/dynaum/kubeside/internal/config"
	"github.com/dynaum/kubeside/internal/kubeconfig"
	"github.com/dynaum/kubeside/internal/timeline"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// allowSecrets makes the cluster answer a SelfSubjectAccessReview the way a
// real one would for a reader with, or without, get on that Secret.
func allowSecrets(client *fake.Clientset, allowed bool) {
	client.PrependReactor("create", "selfsubjectaccessreviews", func(a ktesting.Action) (bool, runtime.Object, error) {
		review := a.(ktesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
		review.Status.Allowed = allowed
		return true, review, nil
	})
}

func secretFixture() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-stripe", Namespace: "team-a"},
		Data:       map[string][]byte{"STRIPE_SECRET_KEY": []byte("sk_live_donotleak")},
	}
}

// revealService wires a Service to a fake cluster through the connector seam
// the production code already uses, so the permission path under test is the
// real one.
func revealService(t *testing.T, client *fake.Clientset) *Service {
	t.Helper()

	kcfg := &kubeconfig.Config{
		Current:  "qa1",
		Contexts: []kubeconfig.Context{{Name: "qa1", IsCurrent: true, Server: "https://api"}},
	}
	mgr := clusters.New(kcfg, clusters.KubeConnector{
		NewClient: func(kubeconfig.Context, kubeconfig.Options) (kubernetes.Interface, error) {
			return client, nil
		},
	}, clusters.Options{})
	t.Cleanup(mgr.Close)

	return NewService(kcfg, mgr, kubeconfig.Options{}, config.Empty(), time.Second)
}

// The permission is asked of the cluster, about the specific Secret, and a no
// is a refusal that names what is missing.
func TestRevealIsRefusedWithoutGetOnThatSecret(t *testing.T) {
	client := fake.NewSimpleClientset(secretFixture())
	allowSecrets(client, false)
	svc := revealService(t, client)

	_, err := svc.RevealSecret("qa1", "team-a", "payments-stripe", "STRIPE_SECRET_KEY", "checkout")
	if err == nil {
		t.Fatal("a reveal was allowed without permission")
	}
	var forbidden *ForbiddenError
	if !errors.As(err, &forbidden) {
		t.Fatalf("error = %T, want a refusal the transport can turn into a 403", err)
	}
	// The phrasing is the resolver's, shared by every disabled control in the
	// product: the verb, the resource, the name, and where.
	for _, want := range []string{"get", "secrets", "payments-stripe", "team-a"} {
		if !strings.Contains(forbidden.Reason, want) {
			t.Errorf("reason = %q, should mention %q", forbidden.Reason, want)
		}
	}
}

// A refused reveal must not read the Secret at all.
func TestRefusedRevealNeverReadsTheSecret(t *testing.T) {
	client := fake.NewSimpleClientset(secretFixture())
	allowSecrets(client, false)
	reads := 0
	client.PrependReactor("get", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		reads++
		return false, nil, nil
	})
	svc := revealService(t, client)

	_, _ = svc.RevealSecret("qa1", "team-a", "payments-stripe", "STRIPE_SECRET_KEY", "checkout")
	if reads != 0 {
		t.Fatalf("the Secret was read %d times despite the refusal", reads)
	}
}

func TestRevealReturnsTheDecodedValue(t *testing.T) {
	client := fake.NewSimpleClientset(secretFixture())
	allowSecrets(client, true)
	svc := revealService(t, client)

	got, err := svc.RevealSecret("qa1", "team-a", "payments-stripe", "STRIPE_SECRET_KEY", "checkout")
	if err != nil {
		t.Fatalf("RevealSecret: %v", err)
	}
	// Nobody should have to run base64 -d to read their own configuration.
	if got.Value != "sk_live_donotleak" {
		t.Fatalf("value = %q, want the decoded value", got.Value)
	}
}

// Only the key that was asked for comes back, never the rest of the object.
func TestRevealReturnsOnlyTheRequestedKey(t *testing.T) {
	sec := secretFixture()
	sec.Data["OTHER"] = []byte("also-secret")
	client := fake.NewSimpleClientset(sec)
	allowSecrets(client, true)
	svc := revealService(t, client)

	got, err := svc.RevealSecret("qa1", "team-a", "payments-stripe", "STRIPE_SECRET_KEY", "checkout")
	if err != nil {
		t.Fatalf("RevealSecret: %v", err)
	}
	if strings.Contains(got.Value, "also-secret") || got.Key != "STRIPE_SECRET_KEY" {
		t.Fatalf("view = %+v, want only the requested key", got)
	}
}

// Reading a production credential leaves a trace in the same place every other
// change does.
func TestRevealIsRecordedOnTheSessionTimeline(t *testing.T) {
	client := fake.NewSimpleClientset(secretFixture())
	allowSecrets(client, true)
	svc := revealService(t, client)

	if _, err := svc.RevealSecret("qa1", "team-a", "payments-stripe", "STRIPE_SECRET_KEY", "checkout"); err != nil {
		t.Fatalf("RevealSecret: %v", err)
	}

	entries := svc.live.Entries(sessionKey("qa1", "team-a", "checkout"))
	if len(entries) != 1 || entries[0].Kind != timeline.KindReveal {
		t.Fatalf("entries = %+v, want the reveal recorded", entries)
	}
	if !strings.Contains(entries[0].Title, "STRIPE_SECRET_KEY") {
		t.Errorf("title = %q, should name the key", entries[0].Title)
	}
	// An audit trail that leaks what it audits is worse than none.
	if strings.Contains(entries[0].Title+entries[0].Detail, "sk_live") {
		t.Fatalf("the timeline entry carries the value: %+v", entries[0])
	}
}

func TestRevealOfAMissingKeyIsAnError(t *testing.T) {
	client := fake.NewSimpleClientset(secretFixture())
	allowSecrets(client, true)
	svc := revealService(t, client)

	if _, err := svc.RevealSecret("qa1", "team-a", "payments-stripe", "NOPE", "checkout"); err == nil {
		t.Fatal("want an error for a key that is not there")
	}
}

// A TLS key is not text. Rendering its bytes as characters produces garbage and
// hides what the value actually is.
func TestBinarySecretIsDescribedNotRendered(t *testing.T) {
	sec := secretFixture()
	sec.Data["tls.key"] = []byte{0xff, 0xfe, 0x00, 0x01}
	client := fake.NewSimpleClientset(sec)
	allowSecrets(client, true)
	svc := revealService(t, client)

	got, err := svc.RevealSecret("qa1", "team-a", "payments-stripe", "tls.key", "checkout")
	if err != nil {
		t.Fatalf("RevealSecret: %v", err)
	}
	if !got.Binary || got.Value != "" {
		t.Fatalf("view = %+v, want binary described rather than rendered", got)
	}
}

// A cluster that cannot answer the permission question is not a yes.
func TestUnanswerablePermissionIsNotAllowed(t *testing.T) {
	client := fake.NewSimpleClientset(secretFixture())
	client.PrependReactor("create", "selfsubjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("the apiserver is having a moment")
	})
	svc := revealService(t, client)

	_, err := svc.RevealSecret("qa1", "team-a", "payments-stripe", "STRIPE_SECRET_KEY", "checkout")
	var forbidden *ForbiddenError
	if !errors.As(err, &forbidden) {
		t.Fatalf("error = %v, want a refusal when permission cannot be established", err)
	}
}
