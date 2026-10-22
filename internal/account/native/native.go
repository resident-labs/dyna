// Package native reads and writes each harness's native sign-in state.
// Credentials pass through only as store.Credential values. Errors and
// labels returned here must never contain token values or raw native output.
package native

import (
	"context"
	"errors"
	"os"
	"time"

	"dyna-agent/internal/account/store"
)

var (
	ErrNotSignedIn  = errors.New("The harness has no signed-in account.")
	ErrNoIdentity   = errors.New("The signed-in account has no stable identity.")
	ErrUnsupported  = errors.New("The harness does not support this account operation.")
	ErrInvalidGrant = errors.New("The saved sign-in expired. Sign in to this account again.")
	ErrSchema       = errors.New("The saved credential format is not supported by this harness version.")
	// ErrPartialActivation wraps an Activate error raised after native state
	// may already have changed, such as a second file write failing or a
	// directory sync failing after rename. Callers then roll back.
	ErrPartialActivation = errors.New("The account change was only partly written.")
)

// Caps states what an adapter can do, with evidence-backed scope text.
type Caps struct {
	Activate      bool   // can write native sign-in state from a saved credential
	IsolatedLogin bool   // LoginCommand can sign in against a temporary root
	Refresh       bool   // Refresh can renew an inactive saved grant
	Scope         string // what a switch changes, e.g. "All Claude Code processes for this user"
	RunningEffect string // what running processes do after a switch, or "unknown"
}

// Captured is one signed-in account read from a native root. Integration is
// the OpenCode provider ID and is empty for single-account harnesses.
type Captured struct {
	Integration string
	Identity    string // "<provider>:<stable provider account ID>"; never an email or label
	Label       string // default display label (email or organization); private
	Credential  store.Credential
	ExpiresAt   time.Time // access-token expiry when known
}

// Host locates native state. The zero value uses the real user environment
// and finds the executable on PATH. Tests set Home and Env so no adapter
// touches real sign-in state.
type Host struct {
	// OpenCodeService uses the managed service in a prepared private scope.
	OpenCodeService bool
	Executable      string
	Home            string
	Env             func(string) string
}

func (h Host) HomeDir() string {
	if h.Home != "" {
		return h.Home
	}
	home, _ := os.UserHomeDir()
	return home
}

func (h Host) Getenv(key string) string {
	if h.Env != nil {
		return h.Env(key)
	}
	return os.Getenv(key)
}

// Manager is implemented once per harness.
type Manager interface {
	Harness() string
	Caps() Caps
	// Capture reads every signed-in account, one per integration. An empty root
	// means the real native location; a non-empty root is an isolated login
	// directory from LoginCommand. No signed-in account returns ErrNotSignedIn.
	Capture(ctx context.Context, root string) ([]Captured, error)
	// Activate writes c for integration to the real native location. It changes
	// no other integration, preserves unrelated settings in shared files, uses
	// atomic replace and honors native locks. Inside the same native lock span
	// it first captures the outgoing account for that integration and returns
	// it as previous, so a token rotation between capture and write cannot be
	// lost. previous is empty when nothing was signed in. If the write fails,
	// previous is still returned when it was read. Callers persist previous,
	// then verify by Capture and matching Integration plus Identity.
	Activate(ctx context.Context, integration string, c store.Credential) (previous []Captured, err error)
	// Refresh renews an inactive saved grant and returns the rotated credential.
	// Callers must never refresh the active account's saved copy.
	Refresh(ctx context.Context, c Captured) (Captured, error)
	// LoginCommand returns an interactive native login. A non-empty root makes
	// it write only to root; an empty root is an in-place native login that
	// changes the host's active account. env holds overrides merged onto the
	// host environment. It must have no side effects. ErrUnsupported means
	// Dyna cannot run this login. For OpenCode, integration selects the
	// provider; others ignore it.
	LoginCommand(root, integration string) (argv []string, env []string, err error)
}

// NativeLister is optional. A Manager implements it when the harness keeps
// several saved credentials natively (OpenCode v2). ListNative returns every
// native credential, active or not, for a one-time import. Dyna never
// refreshes here; a native self-refresh of native-owned grants is allowed.
type NativeLister interface {
	ListNative(ctx context.Context) ([]Captured, error)
}

var constructors = map[string]func(Host) Manager{}

// register is called from each adapter file's init.
func register(harness string, fn func(Host) Manager) { constructors[harness] = fn }

// For returns the adapter for harness, or false when none exists.
func For(harness string, h Host) (Manager, bool) {
	fn, ok := constructors[harness]
	if !ok {
		return nil, false
	}
	return fn(h), true
}

// SavedProber reads only the supplied saved credential. It must not write native state.
// The service refreshes and checkpoints inactive grants before calling it.
type SavedProber interface {
	ProbeSaved(context.Context, Captured) (store.Observation, error)
}

type SavedPlanner interface {
	CachedPlan(Captured) *store.Plan
}

// QuotaError contains only status and delay, never a provider response or token.
type QuotaError struct {
	Status     int
	RetryAfter time.Duration
}

func (e *QuotaError) Error() string { return "Account usage is temporarily unavailable." }
