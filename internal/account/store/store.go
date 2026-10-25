// Package store persists Dyna's saved harness accounts on the host.
// Callers expose Metadata to clients; credential payloads stay inside the host.
package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	statewrite "dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

const maxStoreBytes = 16 << 20

type Plan struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}
type Window struct {
	UnavailableReason string   `json:"unavailableReason,omitempty"`
	Period            string   `json:"period,omitempty"`
	PeriodStart       string   `json:"periodStart,omitempty"`
	ID                string   `json:"id"`
	Label             string   `json:"label"`
	UsedPercent       *float64 `json:"usedPercent,omitempty"`
	Used              *float64 `json:"used,omitempty"`
	Limit             *float64 `json:"limit,omitempty"`
	// Balance is the remaining money when a provider reports a balance
	// instead of a quota window. Unit names its currency.
	Balance  *float64 `json:"balance,omitempty"`
	Unit     string   `json:"unit,omitempty"`
	ResetsAt string   `json:"resetsAt,omitempty"`
}

// UsageReason is the classified reason an account has no current usage reading.
// It is typed so the CLI and the web app can show a designed message instead of
// raw reader text.
type UsageReason string

const (
	// UsageReasonNotReadable means the provider does not publish usage this
	// credential can read, so a retry will not help.
	UsageReasonNotReadable UsageReason = "not-readable"
	// UsageReasonExpired means the saved sign-in can no longer read usage. The
	// owner signs in again.
	UsageReasonExpired UsageReason = "expired"
	// UsageReasonSecret means a reader copied the account's credential, or a
	// value that looks like one, into its result. Dyna rejects that result.
	UsageReasonSecret UsageReason = "secret"
	// UsageReasonProviderError means a temporary provider or network failure.
	UsageReasonProviderError UsageReason = "provider-error"
	// UsageReasonRateLimited means the provider asked Dyna to wait.
	UsageReasonRateLimited UsageReason = "rate-limited"
	// UsageReasonNoWindows means the provider answered but reported no windows.
	UsageReasonNoWindows UsageReason = "no-windows"
)

type Observation struct {
	UsageError       string      `json:"usageError,omitempty"`
	Reason           UsageReason `json:"reason,omitempty"`
	Plan             *Plan       `json:"plan,omitempty"`
	Windows          []Window    `json:"windows"`
	ObservedAt       time.Time   `json:"observedAt"`
	RateLimitedUntil *time.Time  `json:"rateLimitedUntil,omitempty"`
	LastAttemptAt    time.Time   `json:"lastAttemptAt,omitempty"`
	Failures         int         `json:"failures,omitempty"`
	Unavailable      bool        `json:"unavailable,omitempty"`
}

type Metadata struct {
	NativeHome     string       `json:"nativeHome,omitempty"`
	Observation    *Observation `json:"observation,omitempty"`
	ID             string       `json:"id"`
	Provider       Provider     `json:"provider"`
	Kind           Kind         `json:"kind"`
	Harness        string       `json:"harness"`
	Integration    string       `json:"integration,omitempty"`
	Identity       string       `json:"identity"`
	Label          string       `json:"label"`
	NeedsLogin     bool         `json:"needsLogin,omitempty"`
	RefreshPending bool         `json:"refreshPending,omitempty"`
	CreatedAt      time.Time    `json:"createdAt"`
	UpdatedAt      time.Time    `json:"updatedAt"`
}

// Provider is the sign-in service an account belongs to. A harness consumes an
// account; it does not own it.
type Provider string

const (
	ProviderAnthropic  Provider = "anthropic"
	ProviderChatGPT    Provider = "chatgpt"
	ProviderKimiCode   Provider = "kimi-code"
	ProviderXAI        Provider = "xai"
	ProviderMetaMuse   Provider = "meta-muse"
	ProviderOpenCodeGo Provider = "opencode-go"
	ProviderDeepSeek   Provider = "deepseek"
	ProviderZAI        Provider = "zai"
)

// Kind is how an account signed in: an interactive sign-in or a pasted key.
type Kind string

const (
	KindOAuth  Kind = "oauth"
	KindAPIKey Kind = "api_key"
)

// ProviderFor maps a saved harness sign-in to its provider and kind.
// Integration names the provider where one harness keeps several; it is empty
// for a harness that holds one. An unmapped pair keeps no provider, so a later
// release can claim it.
func ProviderFor(harness, integration string) (Provider, Kind) {
	switch harness {
	case "claude-code":
		return ProviderAnthropic, KindOAuth
	case "kimi-code":
		return ProviderKimiCode, KindOAuth
	case "grok":
		return ProviderXAI, KindOAuth
	case "pi":
		// Pi keeps every provider in one home, so its row names no provider yet.
		return "", KindOAuth
	}
	return "", ""
}

// Credential is adapter-owned JSON. Schema identifies its native format.
// It must never be returned by an API binding or included in an error.
type Credential struct {
	Schema  string          `json:"schema"`
	Payload json.RawMessage `json:"payload"`
}

type Entry struct {
	Metadata
	Credential Credential `json:"credential"`
}

type State struct {
	Entries []Entry `json:"entries"`
}

type Store struct {
	Root string
	// Quiet keeps this store's writes from publishing Topic. The engine's
	// credential refresh pass sets it: the pass fixes its own next deadline,
	// so its writes must not start another pass before its retry delay.
	Quiet bool
}

// DefaultStore is the host's account store under the Dyna data directory.
func DefaultStore() *Store { return &Store{Root: filepath.Join(store.DataDir(), "accounts")} }

// IsOpenCodeIntegration identifies OpenCode’s own account services.
func IsOpenCodeIntegration(integration string) bool {
	return integration == "opencode-go" || integration == "opencode"
}

// KnownHarness reports whether this release runs h. Every account write path
// validates its harness here, so a row can never name a harness that no code
// runs.
func KnownHarness(h string) bool {
	switch h {
	case "claude-code", "grok", "kimi-code", "muse", "opencode", "pi":
		return true
	}
	return false
}

// AccountsRetired reports whether the owner retired the saved accounts of
// harness h on 2026-10-25, when Pi took over its provider: those sign-ins were
// moved into Pi accounts by hand, so the harness-bound rows are dead weight and
// the migration drops them. Codex has since left KnownHarness, so nothing writes
// a row for it; Muse and OpenCode still run in this release and keep their entry
// until their own removal. A database older than the migration still needs every
// name here, because it upgrades straight to this release. Pi's own accounts for
// chatgpt, meta-muse and opencode-go are not harness-bound and stay.
func AccountsRetired(h string) bool {
	switch h {
	case "codex", "muse", "opencode":
		return true
	}
	return false
}

// Read returns SQL metadata and protected credentials.
func (s *Store) Read() (State, error) {
	db, err := s.db()
	if err != nil {
		return State{}, errors.New("Saved accounts could not be read.")
	}
	ctx := context.Background()
	return s.load(ctx, db)
}

// Update holds the same lock through native activation and persistence when
// necessary. A failed callback leaves its changes uncommitted. The independent
// legacy provider cleanup can persist before the callback.
func (s *Store) Update(fn func(*State) error) error {
	return s.UpdateContext(context.Background(), fn)
}

// UpdateContext saves the callback's changes and request receipt in one SQL
// commit. Import and legacy cleanup remain independent maintenance writes.
// Credential files retain their existing write order.
// With a request-bearing ctx, fn must change metadata only: changed credential
// files are written before SQL checks replay or argument conflicts. A request
// that changes credentials needs a receipt check before any secret write.
func (s *Store) UpdateContext(ctx context.Context, fn func(*State) error) error {
	return s.transaction(ctx, func(state *State, checkpoint func() error) error {
		if err := fn(state); err != nil {
			return err
		}
		return checkpoint()
	})
}

// Transaction lets a switch persist captured or refreshed grants before native
// activation, while retaining the lock. Checkpoints survive a later error.
func (s *Store) Transaction(fn func(*State, func() error) error) error {
	return s.transaction(context.Background(), fn)
}

func (s *Store) transaction(ctx context.Context, fn func(*State, func() error) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.Root == "" {
		return errors.New("Saved account storage is not configured.")
	}
	if err := os.MkdirAll(s.Root, 0700); err != nil {
		return errors.New("Saved account directory could not be created.")
	}
	if err := os.Chmod(s.Root, 0700); err != nil {
		return errors.New("Saved account directory permissions could not be set.")
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	db, err := s.db()
	if err != nil {
		return errors.New("Saved accounts could not be read.")
	}
	// Maintenance must not record the user operation before its callback saves.
	maintenanceCtx := statewrite.ForRequest(ctx, "")
	// A crash during a removal, after its row and before its secret, leaves
	// that secret; drop it before anything else reads the folder.
	if err := s.dropOrphanCredentials(maintenanceCtx, db); err != nil {
		return err
	}
	committed, err := s.load(maintenanceCtx, db)
	if err != nil {
		return err
	}
	state := committed.clone()
	return fn(&state, func() error {
		if err := s.save(ctx, db, committed, state); err != nil {
			return err
		}
		committed = state.clone()
		return nil
	})
}

// lock takes the registry lock that orders every writer of the accounts,
// in the engine and in a runner's failover switch.
func (s *Store) lock() (func(), error) {
	lock, err := os.OpenFile(filepath.Join(s.Root, "accounts.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, errors.New("Saved accounts could not be locked.")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		lock.Close()
		return nil, errors.New("Saved accounts could not be locked.")
	}
	return func() { lock.Close() }, nil
}

// Save updates the same harness/integration/identity in place, retaining its ID
// and custom label. An empty label uses the adapter's current identity label.
func (state *State) Save(harness, integration, identity, label string, credential Credential) (Metadata, error) {
	if harness == "" || identity == "" || credential.Schema == "" || !json.Valid(credential.Payload) {
		return Metadata{}, errors.New("An account identity and valid credentials are required.")
	}
	now := time.Now().UTC()
	for i := range state.Entries {
		entry := &state.Entries[i]
		if entry.Harness != harness || entry.Integration != integration || entry.Identity != identity {
			continue
		}
		entry.Credential = credential
		entry.NeedsLogin = false
		entry.UpdatedAt = now
		// A row saved before the provider model names no provider, so claim it
		// here. A row that already names one keeps it.
		if entry.Provider == "" && entry.Kind == "" {
			entry.Provider, entry.Kind = ProviderFor(harness, integration)
		}
		if entry.Label == "" {
			entry.Label = label
		}
		return entry.Metadata, nil
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return Metadata{}, errors.New("A saved account ID could not be created.")
	}
	provider, kind := ProviderFor(harness, integration)
	meta := Metadata{ID: hex.EncodeToString(id), Provider: provider, Kind: kind, Harness: harness, Integration: integration, Identity: identity, Label: label, CreatedAt: now, UpdatedAt: now}
	state.Entries = append(state.Entries, Entry{Metadata: meta, Credential: credential})
	return meta, nil
}

func (state *State) Find(id string) *Entry {
	for i := range state.Entries {
		if state.Entries[i].ID == id {
			return &state.Entries[i]
		}
	}
	return nil
}

func (state *State) Remove(id string) bool {
	for i := range state.Entries {
		if state.Entries[i].ID == id {
			state.Entries = append(state.Entries[:i], state.Entries[i+1:]...)
			return true
		}
	}
	return false
}
