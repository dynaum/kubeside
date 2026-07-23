package environments

import (
	"reflect"
	"sort"
	"testing"
)

func TestParsePatternRejectsBadTemplates(t *testing.T) {
	for _, tmpl := range []string{"", "   ", "no-placeholders", "{env}-{env}"} {
		if _, err := ParsePattern(tmpl); err == nil {
			t.Errorf("ParsePattern(%q) should error", tmpl)
		}
	}
}

func TestFinalPlaceholderCapturesMultiSegmentService(t *testing.T) {
	p, err := ParsePattern("{env}-{system}-{service}")
	if err != nil {
		t.Fatalf("ParsePattern: %v", err)
	}
	// Service names routinely contain the separator; they must stay whole.
	seg, ok := p.Match("sandbox1-acme-notification-aws-ses-adapter")
	if !ok {
		t.Fatal("expected a match")
	}
	if seg["env"] != "sandbox1" || seg["system"] != "acme" || seg["service"] != "notification-aws-ses-adapter" {
		t.Fatalf("segments = %v", seg)
	}
}

func TestNonFinalPlaceholdersTakeOneSegment(t *testing.T) {
	p, _ := ParsePattern("{env}-{system}-{service}")
	seg, ok := p.Match("qa1-billing-web")
	if !ok {
		t.Fatal("expected a match")
	}
	if seg["env"] != "qa1" || seg["system"] != "billing" || seg["service"] != "web" {
		t.Fatalf("segments = %v", seg)
	}
}

func TestTooFewSegmentsDoesNotMatch(t *testing.T) {
	p, _ := ParsePattern("{env}-{system}-{service}")
	// A two-segment namespace is a platform namespace, not a tenant service.
	if _, ok := p.Match("kube-system"); ok {
		t.Fatal("kube-system should not match a three-segment pattern")
	}
	if _, ok := p.Match("argocd"); ok {
		t.Fatal("a single-segment namespace should not match")
	}
}

func TestLiteralSegmentInTemplate(t *testing.T) {
	// Anchoring the system as a literal excludes platform namespaces directly.
	p, _ := ParsePattern("{env}-acme-{service}")
	if seg, ok := p.Match("sandbox1-acme-web"); !ok || seg["env"] != "sandbox1" || seg["service"] != "web" {
		t.Fatalf("match = %v, %v", seg, ok)
	}
	if _, ok := p.Match("aws-load-balancer-controller"); ok {
		t.Fatal("a platform namespace must not match a system-anchored pattern")
	}
}

// The core finding from the real cluster: a structural rule alone misclassifies
// platform namespaces that happen to have three or more segments.
func TestSystemAnchorSeparatesTenantsFromPlatform(t *testing.T) {
	r, err := NewResolver(Config{
		Pattern: "{env}-{system}-{service}",
		Systems: []string{"acme"},
	})
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}

	tenant := []string{
		"sandbox1-acme-core",
		"sandbox2-acme-core",
		"sandbox1-acme-url-shortener-java",
	}
	platform := []string{
		"aws-load-balancer-controller",
		"aws-efs-csi-driver",
		"eks-ami-updater",
		"kube-system",
		"argocd",
	}

	for _, ns := range tenant {
		d := r.Decompose(ns)
		if !d.Tenant {
			t.Errorf("%s should be a tenant namespace", ns)
		}
		if d.System != "acme" {
			t.Errorf("%s system = %q, want acme", ns, d.System)
		}
	}
	for _, ns := range platform {
		if d := r.Decompose(ns); d.Tenant {
			t.Errorf("%s classified as tenant (env=%s system=%s service=%s); it is platform infrastructure",
				ns, d.Environment, d.System, d.Service)
		}
	}
}

func TestEnvironmentAllowlistAnchor(t *testing.T) {
	// When systems vary but environments are stable, anchor on env instead.
	r, _ := NewResolver(Config{
		Pattern:      "{env}-{system}-{service}",
		Environments: []string{"sandbox1", "sandbox2"},
	})
	if !r.Decompose("sandbox1-billing-web").Tenant {
		t.Error("sandbox1 is in the allowlist")
	}
	if r.Decompose("aws-load-balancer-controller").Tenant {
		t.Error("aws is not an allowed environment")
	}
}

func TestIdentityIsSystemAndServiceNotEnvironment(t *testing.T) {
	r, _ := NewResolver(Config{Pattern: "{env}-{system}-{service}", Systems: []string{"acme"}})

	a := r.Decompose("sandbox1-acme-core")
	b := r.Decompose("sandbox2-acme-core")
	if a.Identity() != b.Identity() {
		t.Fatalf("same service in two environments has different identities: %q vs %q", a.Identity(), b.Identity())
	}
	if a.Identity() != "acme/core" {
		t.Errorf("identity = %q, want acme/core", a.Identity())
	}

	// Different service, same environment: different identity.
	c := r.Decompose("sandbox1-acme-payment")
	if a.Identity() == c.Identity() {
		t.Error("distinct services must have distinct identities")
	}
}

func TestPlatformNamespaceHasNoIdentity(t *testing.T) {
	r, _ := NewResolver(Config{Pattern: "{env}-{system}-{service}", Systems: []string{"acme"}})
	if id := r.Decompose("kube-system").Identity(); id != "" {
		t.Fatalf("platform identity = %q, want empty", id)
	}
}

// The collapse the environment axis enables: many rows become few services
// across several environments.
func TestManyRowsCollapseToFewServices(t *testing.T) {
	r, _ := NewResolver(Config{Pattern: "{env}-{system}-{service}", Systems: []string{"acme"}})

	var namespaces []string
	envs := []string{"sandbox1", "sandbox2", "sandbox3", "sandbox4"}
	services := []string{"core", "payment", "checkin", "messaging", "widget"}
	for _, e := range envs {
		for _, s := range services {
			namespaces = append(namespaces, e+"-acme-"+s)
		}
	}
	namespaces = append(namespaces, "aws-load-balancer-controller", "kube-system")

	identities := map[string]bool{}
	tenantCount := 0
	for _, ns := range namespaces {
		d := r.Decompose(ns)
		if d.Tenant {
			tenantCount++
			identities[d.Identity()] = true
		}
	}
	if tenantCount != 20 {
		t.Fatalf("tenant namespaces = %d, want 20 (4 envs x 5 services)", tenantCount)
	}
	if len(identities) != 5 {
		t.Fatalf("distinct services = %d, want 5", len(identities))
	}
}

func TestInferEnvironmentsRanksBySharedServices(t *testing.T) {
	r, _ := NewResolver(Config{Pattern: "{env}-{system}-{service}", Systems: []string{"acme"}})

	namespaces := []string{
		// Two full environments running the same five services.
		"sandbox1-acme-core", "sandbox1-acme-payment", "sandbox1-acme-checkin",
		"sandbox2-acme-core", "sandbox2-acme-payment", "sandbox2-acme-checkin",
		// A third that only has one service.
		"sandbox3-acme-core",
		// Platform noise that must not appear as an environment.
		"aws-load-balancer-controller",
	}

	got := r.InferEnvironments(namespaces)
	if len(got) != 3 {
		t.Fatalf("candidates = %v, want 3 environments", got)
	}
	// Ranked by distinct services, so the fuller environments come first.
	if got[0].Services < got[len(got)-1].Services {
		t.Errorf("candidates not ranked by evidence: %v", got)
	}
	names := []string{got[0].Name, got[1].Name, got[2].Name}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"sandbox1", "sandbox2", "sandbox3"}) {
		t.Errorf("environment names = %v", names)
	}
	for _, c := range got {
		if c.Name == "aws" {
			t.Error("platform namespace leaked into environment inference")
		}
	}
}

func TestNoAnchorTrustsStructure(t *testing.T) {
	// Documented behaviour: with no anchor the resolver trusts the pattern and
	// will over-match. A caller with platform namespaces should anchor.
	r, _ := NewResolver(Config{Pattern: "{env}-{system}-{service}"})
	if !r.Decompose("aws-load-balancer-controller").Tenant {
		t.Fatal("without an anchor, a three-segment namespace matches structurally")
	}
}
