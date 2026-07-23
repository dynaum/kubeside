package apps

import (
	"strings"
	"testing"
)

// obj is a terse constructor so fixtures read like the cluster they describe.
func obj(kind, ns, name string) Object {
	return Object{Kind: kind, Namespace: ns, Name: name, UID: ns + "/" + kind + "/" + name}
}

func (o Object) withLabels(kv ...string) Object {
	if o.Labels == nil {
		o.Labels = map[string]string{}
	}
	for i := 0; i+1 < len(kv); i += 2 {
		o.Labels[kv[i]] = kv[i+1]
	}
	return o
}

func (o Object) withAnnotations(kv ...string) Object {
	if o.Annotations == nil {
		o.Annotations = map[string]string{}
	}
	for i := 0; i+1 < len(kv); i += 2 {
		o.Annotations[kv[i]] = kv[i+1]
	}
	return o
}

func (o Object) ownedBy(owner Object) Object {
	o.Owners = append(o.Owners, Owner{
		Kind: owner.Kind, Name: owner.Name, UID: owner.UID, Controller: true,
	})
	return o
}

// ownedByMissing models an owner that exists in the cluster but was not in the
// result set, which happens constantly under namespace-scoped RBAC.
func (o Object) ownedByMissing(kind, name string) Object {
	o.Owners = append(o.Owners, Owner{
		Kind: kind, Name: name, UID: "absent/" + kind + "/" + name, Controller: true,
	})
	return o
}

func names(apps []App) []string {
	out := make([]string, 0, len(apps))
	for _, a := range apps {
		out = append(out, a.Key.Namespace+"/"+a.Key.Name)
	}
	return out
}

func find(t *testing.T, apps []App, ns, name string) App {
	t.Helper()
	for _, a := range apps {
		if a.Key.Namespace == ns && a.Key.Name == name {
			return a
		}
	}
	t.Fatalf("app %s/%s not found in %v", ns, name, names(apps))
	return App{}
}

func TestPrecedenceChain(t *testing.T) {
	tests := []struct {
		name       string
		in         Object
		wantName   string
		wantOrigin Origin
	}{
		{
			name: "instance label wins over everything",
			in: obj("Deployment", "team-a", "checkout-7d9f").
				withLabels("app.kubernetes.io/instance", "checkout", "app.kubernetes.io/name", "checkout").
				withAnnotations("meta.helm.sh/release-name", "checkout-release"),
			wantName:   "checkout",
			wantOrigin: OriginRecommendedLabels,
		},
		{
			name: "name label used when instance absent",
			in: obj("Deployment", "team-a", "checkout-7d9f").
				withLabels("app.kubernetes.io/name", "checkout"),
			wantName:   "checkout",
			wantOrigin: OriginRecommendedLabels,
		},
		{
			name: "helm release beats argo",
			in: obj("Deployment", "team-a", "x").
				withAnnotations("meta.helm.sh/release-name", "payments").
				withLabels("argocd.argoproj.io/instance", "payments-argo"),
			wantName:   "payments",
			wantOrigin: OriginHelm,
		},
		{
			name: "argo instance when no labels or helm",
			in: obj("Deployment", "team-b", "x").
				withLabels("argocd.argoproj.io/instance", "notifications"),
			wantName:   "notifications",
			wantOrigin: OriginArgo,
		},
		{
			name:       "bare workload falls back to its own name",
			in:         obj("Deployment", "team-c", "legacy-worker"),
			wantName:   "legacy-worker",
			wantOrigin: OriginWorkloadName,
		},
		{
			name: "blank label value is ignored, not used as a name",
			in: obj("Deployment", "team-c", "legacy-worker").
				withLabels("app.kubernetes.io/instance", "   "),
			wantName:   "legacy-worker",
			wantOrigin: OriginWorkloadName,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Group([]Object{tc.in})
			if len(got) != 1 {
				t.Fatalf("want 1 app, got %d: %v", len(got), names(got))
			}
			if got[0].Key.Name != tc.wantName {
				t.Errorf("name = %q, want %q", got[0].Key.Name, tc.wantName)
			}
			if got[0].Origin != tc.wantOrigin {
				t.Errorf("origin = %s, want %s", got[0].Origin, tc.wantOrigin)
			}
		})
	}
}

// The no-labels cluster. Entire organisations run this way, so the last-resort
// path is first-class rather than an edge case.
func TestNoLabelsNoHelmCluster(t *testing.T) {
	dep := obj("Deployment", "prod", "checkout")
	rs := obj("ReplicaSet", "prod", "checkout-7d9f4b8c6").ownedBy(dep)
	p1 := obj("Pod", "prod", "checkout-7d9f4b8c6-x2m4p").ownedBy(rs)
	p2 := obj("Pod", "prod", "checkout-7d9f4b8c6-q8kzt").ownedBy(rs)

	sts := obj("StatefulSet", "prod", "session-store")
	sp := obj("Pod", "prod", "session-store-0").ownedBy(sts)

	got := Group([]Object{p1, rs, dep, sp, sts, p2})

	if want := []string{"prod/checkout", "prod/session-store"}; !equal(names(got), want) {
		t.Fatalf("apps = %v, want %v", names(got), want)
	}
	checkout := find(t, got, "prod", "checkout")
	if checkout.Origin != OriginWorkloadName {
		t.Errorf("origin = %s, want %s", checkout.Origin, OriginWorkloadName)
	}
	if checkout.Kind != "Deployment" {
		t.Errorf("kind = %q, want Deployment", checkout.Kind)
	}
	if len(checkout.Workloads) != 4 {
		t.Errorf("want deployment+rs+2 pods = 4 workloads, got %d", len(checkout.Workloads))
	}
}

// Collision case one: the same name in two namespaces of one cluster must
// stay two apps. Merging them presents unrelated services as one row.
func TestSameNameDifferentNamespacesStaySeparate(t *testing.T) {
	a := obj("Deployment", "team-a", "api")
	b := obj("Deployment", "team-b", "api")

	got := Group([]Object{a, b})

	if want := []string{"team-a/api", "team-b/api"}; !equal(names(got), want) {
		t.Fatalf("apps = %v, want %v", names(got), want)
	}
}

// Collision case two: two releases of one chart in a single namespace are two
// apps, distinguished by instance while sharing a component name.
func TestTwoReleasesOfOneChartStaySeparate(t *testing.T) {
	blue := obj("Deployment", "shared", "redis-blue").
		withLabels("app.kubernetes.io/name", "redis", "app.kubernetes.io/instance", "redis-blue")
	green := obj("Deployment", "shared", "redis-green").
		withLabels("app.kubernetes.io/name", "redis", "app.kubernetes.io/instance", "redis-green")

	got := Group([]Object{blue, green})

	if want := []string{"shared/redis-blue", "shared/redis-green"}; !equal(names(got), want) {
		t.Fatalf("apps = %v, want %v", names(got), want)
	}
	for _, a := range got {
		if a.Component != "redis" {
			t.Errorf("%s component = %q, want redis", a.Key.Name, a.Component)
		}
	}
}

func TestCronJobAdoptsItsJobsAndPods(t *testing.T) {
	cj := obj("CronJob", "team-c", "billing")
	job := obj("Job", "team-c", "billing-29384").ownedBy(cj)
	pod := obj("Pod", "team-c", "billing-29384-lm4x").ownedBy(job)

	got := Group([]Object{pod, job, cj})

	if want := []string{"team-c/billing"}; !equal(names(got), want) {
		t.Fatalf("apps = %v, want %v", names(got), want)
	}
	app := find(t, got, "team-c", "billing")
	if app.Kind != "CronJob" {
		t.Errorf("kind = %q, want CronJob", app.Kind)
	}
	if len(app.Workloads) != 3 {
		t.Errorf("want cronjob+job+pod = 3, got %d", len(app.Workloads))
	}
}

func TestStandaloneJobIsItsOwnApp(t *testing.T) {
	job := obj("Job", "team-c", "one-off-migration")

	got := Group([]Object{job})

	if want := []string{"team-c/one-off-migration"}; !equal(names(got), want) {
		t.Fatalf("apps = %v, want %v", names(got), want)
	}
}

// Under namespace-scoped RBAC the owner is frequently outside the result set.
// The owner reference still names it, so grouping must use that rather than
// inventing an app per ReplicaSet.
func TestOwnerOutsideResultSetStillGroups(t *testing.T) {
	rs := obj("ReplicaSet", "prod", "checkout-7d9f4b8c6").ownedByMissing("Deployment", "checkout")

	got := Group([]Object{rs})

	if want := []string{"prod/checkout"}; !equal(names(got), want) {
		t.Fatalf("apps = %v, want %v", names(got), want)
	}
	if o := find(t, got, "prod", "checkout").Origin; o != OriginOwnerChain {
		t.Errorf("origin = %s, want %s", o, OriginOwnerChain)
	}
}

// A pod whose ReplicaSet is absent: the RS name carries the pod-template-hash,
// so stripping it recovers the Deployment name instead of creating an app per
// ReplicaSet revision.
func TestPodWithAbsentReplicaSetStripsTemplateHash(t *testing.T) {
	pod := obj("Pod", "prod", "checkout-7d9f4b8c6-x2m4p").
		withLabels("pod-template-hash", "7d9f4b8c6").
		ownedByMissing("ReplicaSet", "checkout-7d9f4b8c6")

	got := Group([]Object{pod})

	if want := []string{"prod/checkout"}; !equal(names(got), want) {
		t.Fatalf("apps = %v, want %v", names(got), want)
	}
}

func TestPodWithAbsentReplicaSetAndNoHashLabelKeepsFullName(t *testing.T) {
	pod := obj("Pod", "prod", "checkout-abc-x2m4p").ownedByMissing("ReplicaSet", "checkout-abc")

	got := Group([]Object{pod})

	// Without the hash label there is nothing safe to strip; guessing would
	// merge unrelated workloads, so the full name is kept.
	if want := []string{"prod/checkout-abc"}; !equal(names(got), want) {
		t.Fatalf("apps = %v, want %v", names(got), want)
	}
}

func TestOwnerCycleDoesNotHang(t *testing.T) {
	a := obj("ReplicaSet", "ns", "a")
	b := obj("ReplicaSet", "ns", "b")
	a.Owners = []Owner{{Kind: "ReplicaSet", Name: "b", UID: b.UID, Controller: true}}
	b.Owners = []Owner{{Kind: "ReplicaSet", Name: "a", UID: a.UID, Controller: true}}

	got := Group([]Object{a, b})

	if len(got) == 0 {
		t.Fatal("cycle produced no apps; want the cycle broken and something emitted")
	}
}

func TestNonControllerOwnerIsNotFollowed(t *testing.T) {
	dep := obj("Deployment", "ns", "owner-app")
	// A plain (non-controller) owner reference, e.g. a ScaledObject annotating
	// ownership, must not pull the workload into that app.
	other := obj("Deployment", "ns", "standalone")
	other.Owners = []Owner{{Kind: "Deployment", Name: dep.Name, UID: dep.UID, Controller: false}}

	got := Group([]Object{dep, other})

	if want := []string{"ns/owner-app", "ns/standalone"}; !equal(names(got), want) {
		t.Fatalf("apps = %v, want %v", names(got), want)
	}
}

func TestHelmReleaseGroupsSeveralWorkloads(t *testing.T) {
	dep := obj("Deployment", "prod", "wordpress").
		withAnnotations("meta.helm.sh/release-name", "blog")
	sts := obj("StatefulSet", "prod", "wordpress-mariadb").
		withAnnotations("meta.helm.sh/release-name", "blog")

	got := Group([]Object{dep, sts})

	if want := []string{"prod/blog"}; !equal(names(got), want) {
		t.Fatalf("apps = %v, want %v", names(got), want)
	}
	app := find(t, got, "prod", "blog")
	if app.Kind != "Deployment" {
		t.Errorf("primary kind = %q, want Deployment (lowest rank wins)", app.Kind)
	}
	if len(app.Workloads) != 2 {
		t.Errorf("want 2 workloads, got %d", len(app.Workloads))
	}
}

func TestGroupIsDeterministic(t *testing.T) {
	in := []Object{
		obj("Deployment", "b", "two"),
		obj("Deployment", "a", "one"),
		obj("Deployment", "a", "zed"),
	}
	first := names(Group(in))
	for i := 0; i < 20; i++ {
		if got := names(Group(in)); !equal(got, first) {
			t.Fatalf("run %d = %v, want stable %v", i, got, first)
		}
	}
	if want := []string{"a/one", "a/zed", "b/two"}; !equal(first, want) {
		t.Fatalf("order = %v, want %v", first, want)
	}
}

func TestGroupDoesNotMutateInput(t *testing.T) {
	in := []Object{obj("Deployment", "ns", "app").withLabels("k", "v")}
	before := in[0].Labels["k"]
	Group(in)
	if in[0].Labels["k"] != before {
		t.Fatal("Group mutated caller labels")
	}
	if len(in) != 1 {
		t.Fatal("Group mutated the input slice")
	}
}

func TestEmptyAndUnknownKinds(t *testing.T) {
	if got := Group(nil); len(got) != 0 {
		t.Fatalf("nil input produced %d apps", len(got))
	}
	// A CRD-backed workload kubeside does not model should not vanish and
	// should not crash the engine.
	got := Group([]Object{obj("SealedSecret", "ns", "creds")})
	if len(got) != 1 || got[0].Key.Name != "creds" {
		t.Fatalf("unknown kind = %v, want ns/creds", names(got))
	}
}

// A realistic mixed cluster: Helm, Argo, recommended labels, and a bare
// legacy workload side by side. This is the shape the kill criterion judges.
func TestMixedRealisticCluster(t *testing.T) {
	var in []Object

	dep := obj("Deployment", "team-a", "checkout").
		withLabels("app.kubernetes.io/name", "checkout", "app.kubernetes.io/instance", "checkout")
	rs := obj("ReplicaSet", "team-a", "checkout-7d9f4b8c6").ownedBy(dep)
	in = append(in, dep, rs,
		obj("Pod", "team-a", "checkout-7d9f4b8c6-x2m4p").ownedBy(rs),
		obj("Pod", "team-a", "checkout-7d9f4b8c6-q8kzt").ownedBy(rs))

	helmDep := obj("Deployment", "team-a", "payments").
		withAnnotations("meta.helm.sh/release-name", "payments", "meta.helm.sh/release-namespace", "team-a")
	in = append(in, helmDep)

	argoDep := obj("Deployment", "team-b", "notifications").
		withLabels("argocd.argoproj.io/instance", "notifications")
	in = append(in, argoDep)

	in = append(in, obj("Deployment", "team-c", "legacy-worker"))

	cj := obj("CronJob", "team-c", "billing")
	job := obj("Job", "team-c", "billing-29384").ownedBy(cj)
	in = append(in, cj, job, obj("Pod", "team-c", "billing-29384-lm4x").ownedBy(job))

	in = append(in, obj("DaemonSet", "team-c", "log-shipper"))

	got := Group(in)

	want := []string{
		"team-a/checkout", "team-a/payments",
		"team-b/notifications",
		"team-c/billing", "team-c/legacy-worker", "team-c/log-shipper",
	}
	if !equal(names(got), want) {
		t.Fatalf("apps = %v\nwant   %v", names(got), want)
	}

	for _, tc := range []struct {
		ns, name string
		origin   Origin
		kind     string
	}{
		{"team-a", "checkout", OriginRecommendedLabels, "Deployment"},
		{"team-a", "payments", OriginHelm, "Deployment"},
		{"team-b", "notifications", OriginArgo, "Deployment"},
		{"team-c", "billing", OriginWorkloadName, "CronJob"},
		{"team-c", "legacy-worker", OriginWorkloadName, "Deployment"},
		{"team-c", "log-shipper", OriginWorkloadName, "DaemonSet"},
	} {
		a := find(t, got, tc.ns, tc.name)
		if a.Origin != tc.origin {
			t.Errorf("%s/%s origin = %s, want %s", tc.ns, tc.name, a.Origin, tc.origin)
		}
		if a.Kind != tc.kind {
			t.Errorf("%s/%s kind = %s, want %s", tc.ns, tc.name, a.Kind, tc.kind)
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestOriginStringsAreStable(t *testing.T) {
	// These strings reach the UI and the docs, so a rename is a breaking change.
	for o, want := range map[Origin]string{
		OriginRecommendedLabels: "recommended-labels",
		OriginHelm:              "helm-release",
		OriginArgo:              "argocd-instance",
		OriginOwnerChain:        "owner-chain",
		OriginWorkloadName:      "workload-name",
	} {
		if got := o.String(); got != want {
			t.Errorf("Origin(%d) = %q, want %q", o, got, want)
		}
	}
	if s := Origin(99).String(); !strings.Contains(s, "unknown") {
		t.Errorf("out-of-range origin = %q, want unknown", s)
	}
}

// Found on a real cluster: a pod owned by an operator's custom resource
// became its own app. The grouping is correct, but the row is infrastructure
// the developer did not deploy, so it is marked rather than hidden.
func TestPodOwnedByCustomResourceIsMarkedManaged(t *testing.T) {
	p := obj("Pod", "arc-runners", "scale-set-abc123-listener").
		ownedByMissing("AutoscalingListener", "scale-set-abc123-listener")

	got := Group([]Object{p})

	if len(got) != 1 {
		t.Fatalf("apps = %v, want 1", names(got))
	}
	a := got[0]
	if a.ManagedBy != "AutoscalingListener" {
		t.Fatalf("ManagedBy = %q, want the controller kind so the UI can de-emphasise it", a.ManagedBy)
	}
	// Hiding would conflict with disable-never-hide, and a CRD-backed workload
	// might genuinely be someone's app.
	if a.Key.Name == "" {
		t.Fatal("the row must still exist")
	}
}

func TestOrdinaryWorkloadsAreNotMarkedManaged(t *testing.T) {
	dep := obj("Deployment", "prod", "checkout")
	rs := obj("ReplicaSet", "prod", "checkout-7d9f").ownedBy(dep)
	pod := obj("Pod", "prod", "checkout-7d9f-x1").ownedBy(rs)

	for _, a := range Group([]Object{dep, rs, pod}) {
		if a.ManagedBy != "" {
			t.Fatalf("%s marked as managed by %q; resolving through a ReplicaSet is ordinary",
				a.Key.Name, a.ManagedBy)
		}
	}
}

func TestAbsentReplicaSetOwnerIsNotMarkedManaged(t *testing.T) {
	// Under namespace-scoped RBAC the Deployment is frequently unreadable.
	// That is a permissions gap, not a custom controller.
	rs := obj("ReplicaSet", "prod", "checkout-7d9f").ownedByMissing("Deployment", "checkout")
	for _, a := range Group([]Object{rs}) {
		if a.ManagedBy != "" {
			t.Fatalf("ManagedBy = %q; an unreadable Deployment is not a CRD", a.ManagedBy)
		}
	}
}

func TestCronJobOwnedJobsAreNotMarkedManaged(t *testing.T) {
	cj := obj("CronJob", "team-c", "billing")
	job := obj("Job", "team-c", "billing-1").ownedBy(cj)
	for _, a := range Group([]Object{cj, job}) {
		if a.ManagedBy != "" {
			t.Fatalf("ManagedBy = %q; a CronJob owning Jobs is ordinary", a.ManagedBy)
		}
	}
}
