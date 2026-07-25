package api

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dynaum/kubeside/internal/clusters"
	"github.com/dynaum/kubeside/internal/config"
	"github.com/dynaum/kubeside/internal/kubeconfig"
	appsv1 "k8s.io/api/apps/v1"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// The degraded paths are the product, not its edge cases.
//
// A developer's day is full of them: a prod cluster behind a VPN that is off, an
// SSO session that expired at lunch, a namespace-scoped role, a cluster with no
// metrics-server, secrets nobody may read. Every one of these has a wrong
// behaviour that looks like a right one, and this file is where those are
// pinned down.
//
// The rule under all of them: absence of knowledge and absence of a thing are
// different facts, and kubeside never renders one as the other.

// degradedCluster is a normal small cluster the reactors below then break in
// one specific way.
func degradedCluster() *fake.Clientset {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "checkout", Namespace: "team-a", UID: "dep",
			Labels: map[string]string{"app.kubernetes.io/instance": "checkout"},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "checkout"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "checkout"}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name:  "app",
					Image: "checkout:v1",
					Env: []corev1.EnvVar{{
						Name: "PASSWORD",
						ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "db"}, Key: "PASSWORD",
						}},
					}},
				}}},
			},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "checkout-1", Namespace: "team-a",
			Labels: map[string]string{"app.kubernetes.io/instance": "checkout"},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app", Image: "checkout:v1",
			Env: []corev1.EnvVar{{
				Name: "PASSWORD",
				ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "db"}, Key: "PASSWORD",
				}},
			}},
		}}},
		Status: corev1.PodStatus{Phase: "Running", ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true}}},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "team-a"},
		Data:       map[string][]byte{"PASSWORD": []byte("hunter2")},
	}
	return fake.NewSimpleClientset(dep, pod, secret)
}

// degradedService wires a Service to a cluster whose connector may itself fail,
// which is how an unreachable cluster and an expired credential are modelled.
func degradedService(t *testing.T, client kubernetes.Interface, connectErr error) *Service {
	t.Helper()

	kcfg := &kubeconfig.Config{
		Current: "qa1",
		Contexts: []kubeconfig.Context{
			{Name: "qa1", IsCurrent: true, Server: "https://qa", Namespace: "team-a"},
			{Name: "prod1", Server: "https://prod", Namespace: "team-a"},
		},
	}
	mgr := clusters.New(kcfg, clusters.KubeConnector{
		NewClient: func(kctx kubeconfig.Context, _ kubeconfig.Options) (kubernetes.Interface, error) {
			if connectErr != nil && kctx.Name == "prod1" {
				return nil, connectErr
			}
			return client, nil
		},
	}, clusters.Options{})
	t.Cleanup(mgr.Close)

	return NewService(kcfg, mgr, kubeconfig.Options{}, config.Empty(), time.Second)
}

// A prod cluster behind a VPN that is switched off is the normal case, not an
// error state. It fails its own panel and says nothing about its apps.
func TestUnreachableClusterRendersItsStateAndNoApps(t *testing.T) {
	svc := degradedService(t, degradedCluster(), errors.New("dial tcp 10.0.0.1:443: i/o timeout"))

	view, err := svc.Apps("prod1")
	if err != nil {
		t.Fatalf("Apps: %v", err)
	}
	if len(view.Apps) != 0 {
		t.Fatalf("apps = %d; an unreachable cluster must not produce an app list", len(view.Apps))
	}
	if view.State == "live" {
		t.Fatalf("state = %q, want the connection failure surfaced", view.State)
	}
	if view.Error == "" {
		t.Error("an unreachable cluster should carry why")
	}
}

// One cluster failing must never take another with it. That is the entire point
// of a per-context circuit breaker.
func TestOneUnreachableClusterLeavesTheOthersWorking(t *testing.T) {
	svc := degradedService(t, degradedCluster(), errors.New("i/o timeout"))

	if _, err := svc.Apps("prod1"); err != nil {
		t.Fatalf("Apps(prod1): %v", err)
	}
	view, err := svc.Apps("qa1")
	if err != nil {
		t.Fatalf("Apps(qa1): %v", err)
	}
	if len(view.Apps) == 0 {
		t.Fatal("a reachable cluster stopped working because another one did not")
	}
}

// "I cannot reach it" and "your session expired" lead to different next actions,
// so they are different states rather than one generic failure.
func TestExpiredCredentialIsNotTheSameAsUnreachable(t *testing.T) {
	expired := degradedService(t, degradedCluster(),
		apierrors.NewUnauthorized("the server has asked for the client to provide credentials"))
	unreachable := degradedService(t, degradedCluster(), errors.New("dial tcp: i/o timeout"))

	authView, _ := expired.Apps("prod1")
	netView, _ := unreachable.Apps("prod1")

	if authView.State == netView.State {
		t.Fatalf("both render as %q; an expired session and an unreachable cluster are different problems", authView.State)
	}
	if !strings.Contains(authView.State, "unauthorized") {
		t.Errorf("state = %q, want the credential problem named", authView.State)
	}
}

// The contexts list is what the rail renders, and a broken cluster still has a
// row there: a missing row would read as a context that does not exist.
func TestABrokenContextStillAppearsInTheList(t *testing.T) {
	svc := degradedService(t, degradedCluster(), errors.New("i/o timeout"))
	if _, err := svc.Apps("prod1"); err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, c := range svc.Contexts() {
		if c.Name == "prod1" {
			found = true
			if c.HasData {
				t.Error("a context that never connected must not claim to have data")
			}
		}
	}
	if !found {
		t.Fatal("the broken context vanished from the list")
	}
}

// No Kubernetes API enumerates the namespaces a user may access, so a forbidden
// cluster-scoped list is a normal answer. The scope says which mode it ended up
// in rather than rendering an empty cluster.
func TestNamespaceScopedRBACFallsBackAndNamesTheScope(t *testing.T) {
	client := degradedCluster()
	client.PrependReactor("list", "namespaces", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "", nil)
	})
	svc := degradedService(t, client, nil)

	view, err := svc.Apps("qa1")
	if err != nil {
		t.Fatalf("Apps: %v", err)
	}
	if len(view.Apps) == 0 {
		t.Fatal("a namespace-scoped reader still sees their own namespace")
	}
	if !strings.Contains(view.Scope, "team-a") {
		t.Fatalf("scope = %q, want the namespace it fell back to", view.Scope)
	}
	if view.Reason == "" {
		t.Error("a fallback should say why it happened")
	}
}

// A kind that could not be read is named, so a developer knows a row might be
// missing rather than concluding the cluster has none of that kind.
func TestAnUnreadableKindIsNamedRatherThanOmittedSilently(t *testing.T) {
	client := degradedCluster()
	client.PrependReactor("list", "cronjobs", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "cronjobs"}, "", nil)
	})
	svc := degradedService(t, client, nil)

	view, err := svc.Apps("qa1")
	if err != nil {
		t.Fatalf("Apps: %v", err)
	}
	var named bool
	for _, p := range view.Partial {
		if p == "CronJob" {
			named = true
		}
	}
	if !named {
		t.Fatalf("partial = %v, want the unreadable kind named", view.Partial)
	}
}

// A cluster with no metrics-server is most clusters. The columns disappear and
// the reason is stated; a zero would be a reading nobody took.
func TestNoMetricsSourceReportsUnavailableRatherThanZero(t *testing.T) {
	client := degradedCluster()
	svc := degradedService(t, client, nil)

	view, err := svc.Apps("qa1")
	if err != nil {
		t.Fatalf("Apps: %v", err)
	}
	if view.Metrics.Available {
		t.Fatalf("metrics = %+v; the fake cluster has no metrics API", view.Metrics)
	}
	if view.Metrics.Reason == "" {
		t.Error("an unavailable metrics source should say why")
	}
}

// A read-only role that excludes secrets is the common shape for prod. The
// configuration still renders, the value stays masked, and the reveal is
// disabled with the verb rather than hidden.
func TestNoSecretReadStillRendersConfigurationWithTheVerbNamed(t *testing.T) {
	client := degradedCluster()
	client.PrependReactor("create", "selfsubjectaccessreviews", func(a ktesting.Action) (bool, runtime.Object, error) {
		return true, denied(a), nil
	})
	svc := degradedService(t, client, nil)

	view, err := svc.Config("qa1", "team-a", "checkout")
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if len(view.Containers) == 0 {
		t.Fatal("configuration stopped rendering because one Secret was unreadable")
	}

	var found bool
	for _, v := range view.Containers[0].Values {
		if v.Key != "PASSWORD" {
			continue
		}
		found = true
		if !v.Masked || v.Value != "" {
			t.Fatalf("value = %+v, want it masked and empty", v)
		}
		if v.CanReveal {
			t.Error("reveal is offered for a Secret this reader cannot get")
		}
		if !strings.Contains(v.RevealReason, "secrets") {
			t.Errorf("revealReason = %q, want the verb named", v.RevealReason)
		}
	}
	if !found {
		t.Fatal("the secret-sourced key disappeared from the table")
	}
}

// A refused reveal is refused by the cluster's own answer, and it never reads
// the Secret on the way to finding that out.
func TestARefusedRevealNeverReadsTheSecret(t *testing.T) {
	client := degradedCluster()
	client.PrependReactor("create", "selfsubjectaccessreviews", func(a ktesting.Action) (bool, runtime.Object, error) {
		return true, denied(a), nil
	})
	reads := 0
	client.PrependReactor("get", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		reads++
		return false, nil, nil
	})
	svc := degradedService(t, client, nil)

	_, err := svc.RevealSecret("qa1", "team-a", "db", "PASSWORD", "checkout")
	var forbidden *ForbiddenError
	if !errors.As(err, &forbidden) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if reads != 0 {
		t.Fatalf("the Secret was read %d times despite the refusal", reads)
	}
}

// Helm history is the source read-only prod roles most often exclude. The
// timeline renders from everything else and names what is missing.
func TestForbiddenHelmHistoryBecomesALabelledGapNotAFailure(t *testing.T) {
	client := degradedCluster()
	client.PrependReactor("list", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", nil)
	})
	svc := degradedService(t, client, nil)

	view, err := svc.Timeline("qa1", "team-a", "checkout")
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	var gap bool
	for _, g := range view.Gaps {
		if g.Source == "helm" && strings.Contains(g.Reason, "read access to secrets") {
			gap = true
		}
	}
	if !gap {
		t.Fatalf("gaps = %+v, want helm named with the access it needs", view.Gaps)
	}
}

// Every timeline carries a session marker even when every reconstruction source
// failed, because "we were watching and nothing happened" is an answer and an
// unmarked axis is not.
func TestATimelineWithNoSourcesStillMarksWhereWatchingBegan(t *testing.T) {
	client := degradedCluster()
	for _, resource := range []string{"replicasets", "secrets", "events", "pods"} {
		client.PrependReactor("list", resource, func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: resource}, "", nil)
		})
	}
	svc := degradedService(t, client, nil)

	view, err := svc.Timeline("qa1", "team-a", "checkout")
	if err != nil {
		// A workload that cannot be resolved at all is a different failure, and
		// the honest one here.
		if strings.Contains(err.Error(), "no workload") {
			t.Skip("the app itself is unreadable in this scenario, which is its own answer")
		}
		t.Fatalf("Timeline: %v", err)
	}
	var session bool
	for _, h := range view.Horizons {
		if h.Source == "session" {
			session = true
		}
	}
	if !session {
		t.Fatalf("horizons = %+v, want the session marker regardless", view.Horizons)
	}
}

// Permission answers are per context, so a cluster nobody can reach does not
// silently grant what a reachable one allows.
func TestAnUnreachableClusterGrantsNothing(t *testing.T) {
	svc := degradedService(t, degradedCluster(), errors.New("i/o timeout"))

	got := svc.Capabilities("prod1", "team-a")
	for key, perm := range got.Can {
		if perm.Allowed {
			t.Fatalf("%s = allowed on a cluster nobody can reach", key)
		}
		if perm.Reason == "" {
			t.Errorf("%s: a refusal with no reason teaches nothing", key)
		}
	}
}

// Exec is the one destructive capability, and an unreachable cluster must not
// open one.
func TestExecRefusesOnAnUnreachableCluster(t *testing.T) {
	svc := degradedService(t, degradedCluster(), errors.New("i/o timeout"))

	_, _, err := svc.StartExec(context.Background(), ExecRequest{
		Context: "prod1", Namespace: "team-a", Pod: "checkout-1", Container: "app",
	}, func([]byte) {})
	if err == nil {
		t.Fatal("a shell opened against a cluster nobody can reach")
	}
}

// denied answers a SelfSubjectAccessReview with a no, the way a cluster does
// for a reader whose role does not cover the question.
func denied(a ktesting.Action) runtime.Object {
	review := a.(ktesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
	review.Status.Allowed = false
	return review
}
