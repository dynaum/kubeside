package rbac

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeReviewer answers access reviews the way a cluster would, and counts them
// so the cache can be checked.
type fakeReviewer struct {
	mu      sync.Mutex
	allow   map[string]bool
	err     error
	reviews int
}

func (f *fakeReviewer) Review(_ context.Context, contextName string, a Action) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reviews++
	if f.err != nil {
		return false, f.err
	}
	return f.allow[contextName+"|"+a.Key()], nil
}

func (f *fakeReviewer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reviews
}

func exec(ns string) Action {
	return Action{Verb: "create", Resource: "pods", Subresource: "exec", Namespace: ns}
}

func TestAnAllowedActionIsPermitted(t *testing.T) {
	f := &fakeReviewer{allow: map[string]bool{"qa|create pods/exec in team-a": true}}
	r := New(f)

	got := r.Can(context.Background(), "qa", exec("team-a"))
	if !got.Allowed {
		t.Fatalf("permission = %+v, want allowed", got)
	}
}

// The whole point of resolving per context: the same control on the same screen
// is enabled in qa and disabled in prod, in one session.
func TestPermissionsAreResolvedPerContext(t *testing.T) {
	f := &fakeReviewer{allow: map[string]bool{"qa|create pods/exec in team-a": true}}
	r := New(f)

	if !r.Can(context.Background(), "qa", exec("team-a")).Allowed {
		t.Error("qa should allow exec")
	}
	if r.Can(context.Background(), "prod", exec("team-a")).Allowed {
		t.Error("prod must not inherit qa's permission")
	}
}

// Disabled, never hidden, with the missing verb named. A control that vanishes
// teaches nothing; one that says "needs create on pods/exec" is actionable.
func TestADeniedPermissionNamesTheVerb(t *testing.T) {
	r := New(&fakeReviewer{allow: map[string]bool{}})

	got := r.Can(context.Background(), "prod", exec("team-a"))
	if got.Allowed {
		t.Fatal("want denied")
	}
	if !strings.Contains(got.Reason, "create") || !strings.Contains(got.Reason, "pods/exec") {
		t.Fatalf("reason = %q, should name the verb and the resource", got.Reason)
	}
	if !strings.Contains(got.Reason, "team-a") {
		t.Errorf("reason = %q, should name the namespace it was refused in", got.Reason)
	}
}

// Every screen asks about the same handful of verbs, so asking the cluster once
// per session is the difference between a responsive UI and a chatty one.
func TestAnswersAreCachedForTheSession(t *testing.T) {
	f := &fakeReviewer{allow: map[string]bool{"qa|create pods/exec in team-a": true}}
	r := New(f)

	for i := 0; i < 5; i++ {
		r.Can(context.Background(), "qa", exec("team-a"))
	}
	if f.count() != 1 {
		t.Fatalf("asked the cluster %d times, want once", f.count())
	}
}

func TestCacheKeysOnEveryPartOfTheQuestion(t *testing.T) {
	f := &fakeReviewer{allow: map[string]bool{}}
	r := New(f)

	r.Can(context.Background(), "qa", exec("team-a"))
	r.Can(context.Background(), "qa", exec("team-b"))
	r.Can(context.Background(), "prod", exec("team-a"))
	r.Can(context.Background(), "qa", Action{Verb: "delete", Resource: "pods", Namespace: "team-a"})

	if f.count() != 4 {
		t.Fatalf("reviews = %d, want one per distinct question", f.count())
	}
}

// A cluster that cannot answer is not a yes. The control stays disabled and
// says the permission could not be established.
func TestAnUnanswerableReviewIsNotAllowed(t *testing.T) {
	r := New(&fakeReviewer{err: errors.New("apiserver unreachable")})

	got := r.Can(context.Background(), "prod", exec("team-a"))
	if got.Allowed {
		t.Fatal("an error must not read as permission")
	}
	if !strings.Contains(got.Reason, "could not") {
		t.Errorf("reason = %q, should say the check failed rather than that access is denied", got.Reason)
	}
}

// A failed check must not be cached: the next screen should ask again rather
// than serve a network blip for the rest of the session.
func TestFailedChecksAreNotCached(t *testing.T) {
	f := &fakeReviewer{err: errors.New("boom")}
	r := New(f)

	r.Can(context.Background(), "prod", exec("team-a"))
	r.Can(context.Background(), "prod", exec("team-a"))

	if f.count() != 2 {
		t.Fatalf("reviews = %d; a failure was cached", f.count())
	}
}

// Screens ask about several verbs at once, and one round trip per verb makes a
// screen feel slow.
func TestCanAllResolvesEveryActionAtOnce(t *testing.T) {
	f := &fakeReviewer{allow: map[string]bool{
		"prod|create pods/exec in team-a": false,
		"prod|delete pods in team-a":      true,
	}}
	r := New(f)

	got := r.CanAll(context.Background(), "prod", []Action{
		exec("team-a"),
		{Verb: "delete", Resource: "pods", Namespace: "team-a"},
	})

	if len(got) != 2 {
		t.Fatalf("permissions = %+v", got)
	}
	if got[exec("team-a").Key()].Allowed {
		t.Error("exec should be denied")
	}
	if !got[Action{Verb: "delete", Resource: "pods", Namespace: "team-a"}.Key()].Allowed {
		t.Error("delete should be allowed")
	}
}

func TestActionKeyReadsLikeTheQuestionItAsks(t *testing.T) {
	if got := exec("team-a").Key(); got != "create pods/exec in team-a" {
		t.Fatalf("key = %q", got)
	}
	if got := (Action{Verb: "get", Resource: "secrets", Name: "db", Namespace: "team-a"}).Key(); got != "get secrets/db in team-a" {
		t.Fatalf("key = %q", got)
	}
	if got := (Action{Verb: "list", Resource: "nodes"}).Key(); got != "list nodes cluster-wide" {
		t.Fatalf("key = %q", got)
	}
}

// The cache is per session and dies with the process, like everything else.
func TestCacheCanBeClearedWhenAContextReconnects(t *testing.T) {
	f := &fakeReviewer{allow: map[string]bool{"qa|create pods/exec in team-a": true}}
	r := New(f)

	r.Can(context.Background(), "qa", exec("team-a"))
	// A reconnect can mean new credentials, and new credentials can mean
	// different permissions.
	r.Forget("qa")
	r.Can(context.Background(), "qa", exec("team-a"))

	if f.count() != 2 {
		t.Fatalf("reviews = %d, want the answer re-asked after a reconnect", f.count())
	}
}

func TestForgettingOneContextLeavesTheOthers(t *testing.T) {
	f := &fakeReviewer{allow: map[string]bool{}}
	r := New(f)

	r.Can(context.Background(), "qa", exec("team-a"))
	r.Can(context.Background(), "prod", exec("team-a"))
	r.Forget("qa")
	r.Can(context.Background(), "prod", exec("team-a"))

	if f.count() != 2 {
		t.Fatalf("reviews = %d; forgetting qa disturbed prod", f.count())
	}
}

// Concurrent screens asking the same question must not each produce a review.
func TestConcurrentAsksShareOneReview(t *testing.T) {
	f := &fakeReviewer{allow: map[string]bool{"qa|create pods/exec in team-a": true}}
	r := New(f)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Can(context.Background(), "qa", exec("team-a"))
		}()
	}
	wg.Wait()

	// Some overlap is acceptable, but twenty screens must not mean twenty
	// round trips.
	if f.count() > 5 {
		t.Fatalf("reviews = %d for one question asked concurrently", f.count())
	}
	_ = time.Now
}
