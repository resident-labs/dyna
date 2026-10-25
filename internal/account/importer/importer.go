// Package importer moves a saved native harness sign-in into a Pi provider
// account. A move creates the Pi account with its own home, verifies the saved
// login, then retires the source row, so Dyna never keeps two saved copies that
// renew one refresh token.
package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"dyna-agent/internal/account/reposcope"
	"dyna-agent/internal/account/store"
)

// Reason is why one source sign-in or one harness was not imported.
type Reason string

const (
	// ReasonInUse marks a harness that an enabled profile or an open session uses.
	ReasonInUse Reason = "harness-in-use"
	// ReasonAntigravity marks the removed Antigravity harness, whose sign-in no
	// Pi provider can take.
	ReasonAntigravity Reason = "antigravity-removed"
	// ReasonUnsupportedHarness marks a harness this importer does not move.
	ReasonUnsupportedHarness Reason = "unsupported-harness"
	// ReasonUnreadable marks a saved credential this release cannot decode.
	ReasonUnreadable Reason = "unreadable-credential"
	// ReasonNoAPIKey marks a saved key account whose key is missing.
	ReasonNoAPIKey Reason = "no-saved-key"
	// ReasonMoveFailed marks a saved sign-in whose Pi write or retirement
	// failed. Detail says what failed; the source stayed in place.
	ReasonMoveFailed Reason = "move-failed"
)

// Refused names one sign-in or one harness that the import left in place.
// Profiles and Sessions name the users of a harness that is still in use.
// Detail explains a failed move, and never carries a secret.
type Refused struct {
	Source   string   `json:"source,omitempty"`
	Label    string   `json:"label,omitempty"`
	Detail   string   `json:"detail,omitempty"`
	Reason   Reason   `json:"reason"`
	Profiles []string `json:"profiles,omitempty"`
	Sessions []string `json:"sessions,omitempty"`
}

// Describe renders one refusal for a person. A refusal never carries a secret.
func (r Refused) Describe(harness string) string {
	switch r.Reason {
	case ReasonInUse:
		return fmt.Sprintf("Dyna did not import %s: an enabled profile or an open session still runs on it (%s).", harness, strings.Join(append(slices.Clone(r.Profiles), r.Sessions...), ", "))
	case ReasonAntigravity:
		return "Dyna cannot import Antigravity: Pi has no provider for its sign-in, and Dyna no longer runs the harness."
	case ReasonUnsupportedHarness:
		return fmt.Sprintf("Dyna cannot import %s: it has no Pi provider for that sign-in.", harness)
	case ReasonNoAPIKey:
		return fmt.Sprintf("%s holds no saved API key to move.", sourceName(r))
	case ReasonMoveFailed:
		return fmt.Sprintf("%s did not move and stays in place: %s", sourceName(r), r.Detail)
	default:
		return fmt.Sprintf("Dyna could not read the saved sign-in of %s.", sourceName(r))
	}
}

func sourceName(r Refused) string {
	switch {
	case r.Label != "":
		return r.Label
	case r.Source != "":
		return "account " + r.Source
	default:
		return "the source"
	}
}

// Moved is one sign-in that the import moved, or would move in a dry run.
type Moved struct {
	Source   string         `json:"source"`
	Label    string         `json:"label"`
	Account  string         `json:"account,omitempty"`
	Provider store.Provider `json:"provider"`
	Kind     store.Kind     `json:"kind"`
}

// Result is the typed answer of an import: what moved and what stayed.
type Result struct {
	Harness string    `json:"harness"`
	DryRun  bool      `json:"dryRun"`
	Moved   []Moved   `json:"moved"`
	Refused []Refused `json:"refused"`
}

// Use names who runs a harness now: an enabled profile or an open session.
// The engine reads both, because the importer holds no profile or session
// storage.
type Use struct {
	Profiles []UseProfile
	Sessions []UseSession
}

// UseProfile is one enabled profile that runs on a harness.
type UseProfile struct {
	Name    string
	Harness string
}

// UseSession is one open session that runs on a harness.
type UseSession struct {
	Ref     string
	Harness string
}

// Refusal reports whether use keeps harness in place. It names every user.
func (u Use) Refusal(harness string) *Refused {
	refused := Refused{Reason: ReasonInUse}
	for _, p := range u.Profiles {
		if p.Harness == harness {
			refused.Profiles = append(refused.Profiles, p.Name)
		}
	}
	for _, s := range u.Sessions {
		if s.Harness == harness {
			refused.Sessions = append(refused.Sessions, s.Ref)
		}
	}
	if len(refused.Profiles) == 0 && len(refused.Sessions) == 0 {
		return nil
	}
	slices.Sort(refused.Profiles)
	slices.Sort(refused.Sessions)
	return &refused
}

// PiAccount is one Pi account a host created for a moved sign-in.
type PiAccount struct {
	ID       string
	Provider store.Provider
	Kind     store.Kind
}

// PiLogin is one Pi sign-in entry to write into a new account's own home.
type PiLogin struct {
	EntryID string
	Entry   json.RawMessage
	Repos   []string
}

// Host writes the Pi side of an import and retires the source.
type Host interface {
	// CreatePiAccount creates a Pi account of provider, writes exactly login as
	// its saved sign-in, reads the account back, and returns it. A failed step
	// leaves no account behind.
	CreatePiAccount(ctx context.Context, provider store.Provider, kind store.Kind, label string, login PiLogin) (PiAccount, error)
	// RemovePiAccount deletes an account that CreatePiAccount made.
	RemovePiAccount(ctx context.Context, id string) error
	// SetRepos replaces one account's repository rules.
	SetRepos(id string, repos []string) error
	// RetireSource deletes a source account and every row that depends on it.
	RetireSource(ctx context.Context, id string) error
}

// Request is one import.
type Request struct {
	Harness string
	Account string
	DryRun  bool
	Use     Use
}

// Importer moves source sign-ins into Pi accounts.
type Importer struct {
	Store *store.Store
	Host  Host
}

// Run moves the requested sign-ins. It refuses a harness that is still in use.
func (im *Importer) Run(ctx context.Context, req Request) (Result, error) {
	result := Result{Harness: req.Harness, DryRun: req.DryRun, Moved: []Moved{}, Refused: []Refused{}}
	if !Importable(req.Harness) {
		return result, fmt.Errorf("Dyna has no import for harness %q. Choose claude-code, codex, kimi-code, grok, muse or opencode.", req.Harness)
	}
	if refusal := req.Use.Refusal(req.Harness); refusal != nil {
		result.Refused = append(result.Refused, *refusal)
		return result, nil
	}
	sources, rules, err := im.sources(req)
	if err != nil {
		return result, err
	}
	if len(sources) == 0 {
		return result, fmt.Errorf("Dyna holds no saved %s sign-in to import.", req.Harness)
	}
	if req.Harness == "antigravity" {
		for _, source := range sources {
			result.Refused = append(result.Refused, Refused{Source: source.ID, Label: source.Label, Reason: ReasonAntigravity})
		}
		return result, nil
	}
	for _, source := range sources {
		login, refusal := convert(ctx, source, rules[source.ID])
		if refusal != nil {
			result.Refused = append(result.Refused, *refusal)
			continue
		}
		if req.DryRun {
			result.Moved = append(result.Moved, Moved{Source: source.ID, Label: source.Label, Provider: login.Provider, Kind: login.Kind})
			continue
		}
		account, err := im.move(ctx, source, login)
		if err != nil {
			// One sign-in that fails does not hide the others: it becomes a
			// refusal, and the sign-ins that moved stay reported.
			result.Refused = append(result.Refused, Refused{Source: source.ID, Label: source.Label, Reason: ReasonMoveFailed, Detail: err.Error()})
			continue
		}
		result.Moved = append(result.Moved, Moved{Source: source.ID, Label: source.Label, Account: account.ID, Provider: login.Provider, Kind: login.Kind})
	}
	return result, nil
}

// move writes and verifies the Pi account, copies its repository rules, and
// only then retires the source. A failure before the retirement removes the Pi
// account, so the source stays the only copy.
func (im *Importer) move(ctx context.Context, source store.Entry, login piSignIn) (PiAccount, error) {
	account, err := im.Host.CreatePiAccount(ctx, login.Provider, login.Kind, source.Label, PiLogin{EntryID: login.EntryID, Entry: login.Entry, Repos: login.Repos})
	if err != nil {
		return PiAccount{}, fmt.Errorf("The %s sign-in of %s could not be written to Pi: %w", login.Provider, sourceName(Refused{Source: source.ID, Label: source.Label}), err)
	}
	if err := im.Host.SetRepos(account.ID, login.Repos); err != nil {
		return PiAccount{}, im.rollback(ctx, account, fmt.Errorf("The repository rules of the moved account could not be copied: %w", err))
	}
	if err := im.Host.RetireSource(ctx, source.ID); err != nil {
		return PiAccount{}, im.rollback(ctx, account, fmt.Errorf("The saved %s sign-in could not be retired: %w", source.Harness, err))
	}
	return account, nil
}

// rollback removes a Pi account that the import made, so a failed move leaves
// the source as the only copy.
func (im *Importer) rollback(ctx context.Context, account PiAccount, cause error) error {
	if err := im.Host.RemovePiAccount(ctx, account.ID); err != nil {
		return errors.Join(cause, fmt.Errorf("The new Pi account could not be removed: %w", err))
	}
	return cause
}

// sources reads the saved sign-ins of the requested harness and their rules.
func (im *Importer) sources(req Request) ([]store.Entry, reposcope.Rules, error) {
	state, err := im.Store.Read()
	if err != nil {
		return nil, nil, err
	}
	rules := reposcope.Rules{}
	if scopes, err := reposcope.Open(filepath.Dir(im.Store.Root)); err == nil {
		rules, _ = scopes.Rules()
	}
	var sources []store.Entry
	for _, entry := range state.Entries {
		if entry.Harness != req.Harness {
			continue
		}
		sources = append(sources, entry)
	}
	if req.Account == "" {
		return sources, rules, nil
	}
	chosen, err := selectSource(sources, req.Account)
	if err != nil {
		return nil, nil, err
	}
	return []store.Entry{chosen}, rules, nil
}

// selectSource resolves an exact ID, an ID prefix, or a label that names one
// saved sign-in.
func selectSource(sources []store.Entry, ref string) (store.Entry, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return store.Entry{}, errors.New("Name an account by ID, ID prefix or label.")
	}
	var prefix, labels []store.Entry
	for _, source := range sources {
		if strings.EqualFold(source.ID, ref) {
			return source, nil
		}
		if strings.HasPrefix(source.ID, ref) {
			prefix = append(prefix, source)
		}
		if strings.EqualFold(source.Label, ref) {
			labels = append(labels, source)
		}
	}
	switch {
	case len(prefix) == 1:
		return prefix[0], nil
	case len(prefix) > 1:
		return store.Entry{}, fmt.Errorf("%s starts %d account IDs. Give more characters of the ID.", ref, len(prefix))
	case len(labels) == 1:
		return labels[0], nil
	case len(labels) > 1:
		return store.Entry{}, fmt.Errorf("%s names %d accounts. Give the account ID.", ref, len(labels))
	}
	return store.Entry{}, fmt.Errorf("No saved sign-in of this harness is called %q.", ref)
}
