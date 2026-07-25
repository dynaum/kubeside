// Package rbac answers what this reader may do, per context.
//
// Every control kubeside disables is disabled because the cluster said so, not
// because a role name looked wrong. The question goes to the apiserver as a
// SelfSubjectAccessReview, which is the only authority on the answer.
//
// The product rule this serves is "disable, never hide". A control that
// vanishes teaches nothing and leaves somebody wondering whether the feature
// exists; one that is visible, disabled, and says "needs create on pods/exec in
// team-a" tells them exactly what to ask for. So every denial carries the verb.
//
// Answers resolve per context. The same control on the same screen is enabled
// in qa and disabled in prod within one session, because permissions are a
// property of the cluster and not of the tool.
package rbac

import (
	"context"
	"fmt"
	"sync"
)

// Action is one question: may I do this verb, to this resource, here.
type Action struct {
	Verb        string
	Resource    string
	Subresource string
	Name        string
	Namespace   string
	Group       string
}

// Key reads like the question it asks, so a cache entry and an error message
// can share one phrasing.
func (a Action) Key() string {
	resource := a.Resource
	if a.Subresource != "" {
		resource += "/" + a.Subresource
	}
	if a.Name != "" {
		resource += "/" + a.Name
	}
	where := "cluster-wide"
	if a.Namespace != "" {
		where = "in " + a.Namespace
	}
	return fmt.Sprintf("%s %s %s", a.Verb, resource, where)
}

// Permission is the answer, with the reason a denial happened.
type Permission struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

// Reviewer asks the cluster. Keeping it an interface lets the caching and the
// phrasing be tested without an apiserver.
type Reviewer interface {
	Review(ctx context.Context, contextName string, a Action) (bool, error)
}

// Resolver caches answers for the session.
type Resolver struct {
	reviewer Reviewer

	mu    sync.Mutex
	cache map[string]map[string]Permission
	// inflight collapses concurrent asks for the same question, so twenty
	// screens opening at once do not become twenty round trips.
	inflight map[string]*call
}

type call struct {
	wg   sync.WaitGroup
	perm Permission
	err  error
}

// New builds a resolver.
func New(r Reviewer) *Resolver {
	return &Resolver{
		reviewer: r,
		cache:    map[string]map[string]Permission{},
		inflight: map[string]*call{},
	}
}

// Can answers one question, asking the cluster at most once per session.
func (r *Resolver) Can(ctx context.Context, contextName string, a Action) Permission {
	key := a.Key()

	r.mu.Lock()
	if perms, ok := r.cache[contextName]; ok {
		if p, ok := perms[key]; ok {
			r.mu.Unlock()
			return p
		}
	}
	full := contextName + "|" + key
	if c, ok := r.inflight[full]; ok {
		r.mu.Unlock()
		c.wg.Wait()
		return c.perm
	}
	c := &call{}
	c.wg.Add(1)
	r.inflight[full] = c
	r.mu.Unlock()

	allowed, err := r.reviewer.Review(ctx, contextName, a)
	switch {
	case err != nil:
		// A cluster that cannot answer is not a yes, and the message says the
		// check failed rather than that access was refused: those are different
		// problems with different fixes.
		c.perm = Permission{Reason: fmt.Sprintf("could not check permission to %s: %v", key, err)}
		c.err = err
	case allowed:
		c.perm = Permission{Allowed: true}
	default:
		c.perm = Permission{Reason: "needs " + key}
	}
	c.wg.Done()

	r.mu.Lock()
	delete(r.inflight, full)
	// A failed check is never cached: the next screen should ask again rather
	// than serve a network blip for the rest of the session.
	if c.err == nil {
		if r.cache[contextName] == nil {
			r.cache[contextName] = map[string]Permission{}
		}
		r.cache[contextName][key] = c.perm
	}
	r.mu.Unlock()

	return c.perm
}

// CanAll answers several questions, which is what a screen actually asks.
func (r *Resolver) CanAll(ctx context.Context, contextName string, actions []Action) map[string]Permission {
	out := make(map[string]Permission, len(actions))
	for _, a := range actions {
		out[a.Key()] = r.Can(ctx, contextName, a)
	}
	return out
}

// Forget drops one context's answers.
//
// A reconnect can mean new credentials, and new credentials can mean different
// permissions. Keeping the old answers would disable a control the user has
// just earned, or worse, enable one they have not.
func (r *Resolver) Forget(contextName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cache, contextName)
}
