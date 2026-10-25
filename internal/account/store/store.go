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
	Unit              string   `json:"unit,omitempty"`
	ResetsAt          string   `json:"resetsAt,omitempty"`
}
type Observation struct {
	UsageError       string     `json:"usageError,omitempty"`
	Plan             *Plan      `json:"plan,omitempty"`
	Windows          []Window   `json:"windows"`
	ObservedAt       time.Time  `json:"observedAt"`
	RateLimitedUntil *time.Time `json:"rateLimitedUntil,omitempty"`
	LastAttemptAt    time.Time  `json:"lastAttemptAt,omitempty"`
	Failures         int        `json:"failures,omitempty"`
	Unavailable      bool       `json:"unavailable,omitempty"`
}

// PrivateProof binds a private sign-in to its account identity.
type PrivateProof struct {
	Version  int    `json:"version"`
	Identity string `json:"identity"`
}

type Metadata struct {
	PrivateProof   *PrivateProof `json:"privateProof,omitempty"`
	NativeHome     string        `json:"nativeHome,omitempty"`
	Observation    *Observation  `json:"observation,omitempty"`
	ID             string        `json:"id"`
	Provider       Provider      `json:"provider"`
	Kind           Kind          `json:"kind"`
	Harness        string        `json:"harness"`
	Integration    string        `json:"integration,omitempty"`
	Identity       string        `json:"identity"`
	Label          string        `json:"label"`
	NeedsLogin     bool          `json:"needsLogin,omitempty"`
	RefreshPending bool          `json:"refreshPending,omitempty"`
	CreatedAt      time.Time     `json:"createdAt"`
	UpdatedAt      time.Time     `json:"updatedAt"`
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
	ProviderGoogle     Provider = "google"
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
	case "codex":
		return ProviderChatGPT, KindOAuth
	case "kimi-code":
		return ProviderKimiCode, KindOAuth
	case "grok":
		return ProviderXAI, KindOAuth
	case "muse":
		return ProviderMetaMuse, KindOAuth
	case "antigravity":
		return ProviderGoogle, KindOAuth
	case "opencode":
		switch integration {
		case "", "opencode", "opencode-go":
			return ProviderOpenCodeGo, KindOAuth
		case "anthropic":
			return ProviderAnthropic, KindAPIKey
		case "openai":
			return ProviderChatGPT, KindAPIKey
		case "xai":
			return ProviderXAI, KindAPIKey
		case "deepseek":
			return ProviderDeepSeek, KindAPIKey
		case "zai":
			return ProviderZAI, KindAPIKey
		}
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
	Version                  int     `json:"version"`
	Entries                  []Entry `json:"entries"`
	OpenCodeProvidersCleaned bool    `json:"openCodeProvidersCleaned,omitempty"`
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

func (st *State) dropOpenCodeProviders() {
	entries := make([]Entry, 0, len(st.Entries))
	for _, e := range st.Entries {
		if e.Harness == "opencode" && e.NativeHome == "" && !IsOpenCodeIntegration(e.Integration) {
			continue
		}
		entries = append(entries, e)
	}
	st.Entries = entries
}

// Read returns SQL metadata and protected credentials after the one-time migration.
func (s *Store) Read() (State, error) {
	db, err := s.db()
	if err != nil {
		return State{}, errors.New("Saved accounts could not be read.")
	}
	ctx := context.Background()
	st, err := s.load(ctx, db)
	if err == nil {
		st.dropOpenCodeProviders()
	}
	return st, err
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
	// TODO: legacy/shim remove later — remove imported third-party OpenCode references once.
	// This only rewrites Dyna's registry. Native credentials and private homes stay intact.
	if !state.OpenCodeProvidersCleaned {
		hadEntries := len(state.Entries) > 0
		state.dropOpenCodeProviders()
		state.OpenCodeProvidersCleaned = true
		if hadEntries {
			if err := s.save(maintenanceCtx, db, committed, state); err != nil {
				return err
			}
			committed = state.clone()
		}
	}
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
