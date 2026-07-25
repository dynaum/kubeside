// Package guard is the ceremony between a developer and a destructive action.
//
// A developer holding a prod context in the same switcher as qa will eventually
// act on the wrong one. Every layer here exists because designing against that
// beats trusting attention:
//
//   - the environment's name travels with every gate, never inferred from
//     memory
//   - the write policy comes from the environment, not from the screen
//   - a confirm environment wants the resource name typed, the pattern people
//     already know from deleting a GitHub repository
//   - break-glass unlocks production for fifteen minutes with a stated reason,
//     then re-locks itself
//   - the blast radius and the equivalent kubectl travel with the gate, so
//     nobody confirms something they have not seen
//   - a gate is always about exactly one environment
//
// One boundary, stated plainly and repeated in the docs: these layers protect
// against accidents, not against intent. The write policy lives in a config
// file the user controls and can edit in a second. RBAC is the security
// boundary; this is ergonomics on top of it and never a substitute for it.
package guard

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dynaum/kubeside/internal/environments"
)

// BreakGlassWindow is how long production stays armed after an unlock. Long
// enough to finish an incident action, short enough that nobody leaves prod
// unlocked by forgetting.
const BreakGlassWindow = 15 * time.Minute

// What the UI must collect before the action may run.
const (
	RequireNothing    = ""
	RequireTypedName  = "typed-name"
	RequireBreakGlass = "break-glass"
)

// Blast is what an action touches.
type Blast struct {
	Pods    int    `json:"pods"`
	Summary string `json:"summary,omitempty"`
	// Unknown marks a radius nobody computed. Rendering it as zero would be a
	// claim nobody made.
	Unknown bool `json:"unknown,omitempty"`
}

// Action is the thing somebody is about to do.
type Action struct {
	Verb      string
	Resource  string
	Name      string
	Namespace string
	// Kubectl is the equivalent command. Showing it is how somebody checks the
	// tool's understanding against their own before agreeing to it.
	Kubectl string
	Blast   Blast
}

// Gate is the answer: whether the action may proceed, and what it costs.
type Gate struct {
	Permitted   bool   `json:"permitted"`
	Require     string `json:"require,omitempty"`
	Confirm     string `json:"confirm,omitempty"`
	Environment string `json:"environment"`
	Risk        string `json:"risk"`
	Policy      string `json:"policy"`
	Reason      string `json:"reason,omitempty"`
	Kubectl     string `json:"kubectl,omitempty"`
	Blast       Blast  `json:"blast"`
	// UnlockedUntil is when a break-glass window closes, empty when locked.
	UnlockedUntil string `json:"unlockedUntil,omitempty"`
}

// Unlock is the record of somebody arming an environment. It belongs on the
// session timeline: arming production is an event, not a setting.
type Unlock struct {
	Context string    `json:"context"`
	Reason  string    `json:"reason"`
	At      time.Time `json:"at"`
	Until   time.Time `json:"until"`
}

// Clock is injected so the window's expiry is tested without waiting.
type Clock interface{ Now() time.Time }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Guard holds the break-glass windows. They live in memory and die with the
// process, like everything else: a tool that remembers prod was unlocked
// yesterday is a tool that unlocks prod tomorrow.
type Guard struct {
	clock Clock

	mu       sync.Mutex
	unlocked map[string]time.Time
}

// New builds a guard.
func New(clock Clock) *Guard {
	if clock == nil {
		clock = realClock{}
	}
	return &Guard{clock: clock, unlocked: map[string]time.Time{}}
}

// Check answers whether one action may proceed in one environment.
func (g *Guard) Check(contextName string, env environments.Environment, a Action) Gate {
	gate := Gate{
		Environment: env.Name,
		Risk:        env.Risk.String(),
		Policy:      env.Write.String(),
		Kubectl:     a.Kubectl,
		Blast:       a.Blast,
	}
	if gate.Blast.Pods == 0 && gate.Blast.Summary == "" {
		gate.Blast.Unknown = true
	}

	switch env.Write {
	case environments.WriteAllow:
		gate.Permitted = true

	case environments.WriteConfirm:
		gate.Permitted = true
		gate.Require = RequireTypedName
		gate.Confirm = a.Name

	case environments.WriteBreakGlass:
		until, open := g.window(contextName)
		if !open {
			gate.Require = RequireBreakGlass
			gate.Reason = g.lockedReason(contextName, env)
			return gate
		}
		// Unlocking is not a licence to stop reading: the typed name stays.
		gate.Permitted = true
		gate.Require = RequireTypedName
		gate.Confirm = a.Name
		gate.UnlockedUntil = until.UTC().Format(time.RFC3339)

	default: // deny
		gate.Reason = fmt.Sprintf(
			"writes are denied in %s by the write policy in your kubeside config; change it there if this environment should allow them",
			env.Name)
	}
	return gate
}

// lockedReason distinguishes never-unlocked from expired, because they lead to
// different next actions.
func (g *Guard) lockedReason(contextName string, env environments.Environment) string {
	g.mu.Lock()
	_, everUnlocked := g.unlocked[contextName]
	g.mu.Unlock()

	if everUnlocked {
		return fmt.Sprintf("the break-glass window for %s has expired; unlock again with a reason to continue", env.Name)
	}
	return fmt.Sprintf("%s is behind break-glass; unlock it with a stated reason to act here", env.Name)
}

// Unlock arms one environment for the break-glass window.
//
// The reason is required and kept. Somebody arming production should have to
// say why, and the record is what makes that legible afterwards.
func (g *Guard) Unlock(contextName, reason string) (Unlock, error) {
	if strings.TrimSpace(contextName) == "" {
		return Unlock{}, fmt.Errorf("unlocking needs an environment: a control never spans more than one")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Unlock{}, fmt.Errorf("unlocking needs a stated reason")
	}

	now := g.clock.Now()
	until := now.Add(BreakGlassWindow)

	g.mu.Lock()
	g.unlocked[contextName] = until
	g.mu.Unlock()

	return Unlock{Context: contextName, Reason: reason, At: now, Until: until}, nil
}

// Lock closes a window early, which is what somebody does when they are done
// rather than waiting out the clock.
func (g *Guard) Lock(contextName string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	// The entry stays, expired, so a later refusal can say the window closed
	// rather than that it never opened.
	g.unlocked[contextName] = g.clock.Now().Add(-time.Second)
}

func (g *Guard) window(contextName string) (time.Time, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	until, ok := g.unlocked[contextName]
	return until, ok && g.clock.Now().Before(until)
}
