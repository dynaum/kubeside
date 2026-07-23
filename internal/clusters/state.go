package clusters

import (
	"errors"
	"os/exec"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// State is what kubeside knows about one cluster right now.
//
// NeverConnected and Stale are deliberately separate. docs/04-multi-cluster.md
// is explicit that "I have not reached this cluster yet" and "here is data from
// 14:02" are different facts, and conflating them during an incident is the
// worst failure this product could have.
type State int

const (
	// StateNeverConnected: no successful connection this session. Nothing is
	// known. Never render this as an empty result set.
	StateNeverConnected State = iota
	// StateConnecting: a connection attempt is in flight.
	StateConnecting
	// StateLive: informers are running and data is current.
	StateLive
	// StateStale: connected earlier, now disconnected. The last snapshot is
	// retained and must always be labelled with its age.
	StateStale
	// StateUnreachable: the attempt failed for a network or DNS reason. Often
	// just a VPN that is off, which is normal rather than exceptional.
	StateUnreachable
	// StateUnauthorized: credentials were rejected or expired. Distinct from
	// unreachable because the fix is different and the UI prompts inline.
	StateUnauthorized
)

func (s State) String() string {
	switch s {
	case StateNeverConnected:
		return "never-connected"
	case StateConnecting:
		return "connecting"
	case StateLive:
		return "live"
	case StateStale:
		return "stale"
	case StateUnreachable:
		return "unreachable"
	case StateUnauthorized:
		return "unauthorized"
	}
	return "unknown"
}

// HasData reports whether anything is known about the cluster. It is the guard
// the UI uses before rendering a panel's contents.
func (s State) HasData() bool { return s == StateLive || s == StateStale }

// Status is a point-in-time snapshot of one connection, safe to hand to the UI.
type Status struct {
	Context  string
	State    State
	LastLive time.Time
	// Age is how old the retained snapshot is. Only meaningful when State is
	// Stale; the UI prints it next to the data.
	Age time.Duration
	Err error
	// RetryAfter is when the circuit breaker will allow another attempt.
	RetryAfter time.Time
}

// classify maps a connection error onto a terminal state.
//
// The default is unreachable, not unauthorized. Most connection failures are a
// VPN that is off or a cluster that is down, and telling a developer their
// credentials expired when the network is simply unavailable sends them to fix
// the wrong thing.
func classify(err error) State {
	if err == nil {
		return StateLive
	}
	if apierrors.IsUnauthorized(err) || apierrors.IsForbidden(err) {
		return StateUnauthorized
	}
	// An exec credential plugin (aws eks get-token, gke-gcloud-auth-plugin,
	// kubelogin) exiting non-zero is a credential failure even though it is
	// not an apierrors type.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return StateUnauthorized
	}
	var authErr *AuthError
	if errors.As(err, &authErr) {
		return StateUnauthorized
	}
	// client-go formats credential plugin failures into a plain string error,
	// so the typed checks above miss them. Matching the message is unpleasant
	// but the alternative is telling someone whose SSO session expired that
	// their cluster is unreachable, which sends them to check the VPN.
	if isCredentialPluginFailure(err.Error()) {
		return StateUnauthorized
	}
	return StateUnreachable
}

// credentialPluginMarkers are substrings client-go uses when an exec
// credential plugin fails. Kept in one place so the heuristic is auditable.
var credentialPluginMarkers = []string{
	"getting credentials",
	"exec plugin",
	"credential plugin",
	"no Auth Provider found",
	"unable to load root certificates",
}

func isCredentialPluginFailure(msg string) bool {
	lower := strings.ToLower(msg)
	for _, m := range credentialPluginMarkers {
		if strings.Contains(lower, strings.ToLower(m)) {
			return true
		}
	}
	return false
}

// AuthError lets a connector report a credential failure that is not an
// apierrors type, such as an exec credential plugin exiting non-zero.
type AuthError struct{ Err error }

func (e *AuthError) Error() string {
	if e.Err == nil {
		return "authentication failed"
	}
	return "authentication failed: " + e.Err.Error()
}

func (e *AuthError) Unwrap() error { return e.Err }
