package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"dyna-agent/internal/account/importer"
	"dyna-agent/internal/account/store"
)

// PiImportHost moves saved native sign-ins into Pi accounts. The importer holds
// the order of the move; this type performs the Pi writes and the source
// retirement.
type PiImportHost struct {
	Runtime *Runtime
}

// CreatePiAccount creates one Pi account whose home holds exactly the given
// provider entry, reads it back, and returns it. A failed step removes the new
// account, so a failed import leaves the source as the only copy.
func (h PiImportHost) CreatePiAccount(ctx context.Context, provider store.Provider, kind store.Kind, label string, login importer.PiLogin) (importer.PiAccount, error) {
	if h.Runtime == nil || h.Runtime.Accounts == nil {
		return importer.PiAccount{}, errors.New("Private account storage is unavailable.")
	}
	// The entry and the account must name one provider: Pi reads the entry by
	// the ID in the login file, and Dyna selects the account by its provider.
	if piProvider(login.EntryID) != provider {
		return importer.PiAccount{}, errors.New("The saved sign-in names a provider that does not match its account.")
	}
	if label == "" {
		label = string(provider) + " login"
	}
	piKeysMu.Lock()
	defer piKeysMu.Unlock()
	release, err := h.Runtime.lockPiAccounts(ctx)
	if err != nil {
		return importer.PiAccount{}, err
	}
	defer release()
	account, err := h.Runtime.Accounts.CreateProvider("pi", provider, kind, label)
	if err != nil {
		return importer.PiAccount{}, err
	}
	fail := func(cause error) (importer.PiAccount, error) {
		if removeErr := h.Runtime.Accounts.Remove(account.ID, true); removeErr != nil {
			return importer.PiAccount{}, errors.Join(cause, removeErr)
		}
		return importer.PiAccount{}, cause
	}
	edit := func(entries map[string]json.RawMessage) {
		clear(entries)
		entries[login.EntryID] = login.Entry
	}
	if _, err := piEditAuth(ctx, account.Home, edit); err != nil {
		return fail(err)
	}
	if _, err := h.Runtime.Accounts.Capture(ctx, account.ID, ""); err != nil {
		return fail(fmt.Errorf("Pi did not save the moved sign-in: %w", err))
	}
	if err := verifyPiLogin(account.Home, login); err != nil {
		return fail(err)
	}
	saved, err := h.Runtime.Accounts.Find(account.ID)
	if err != nil {
		return fail(err)
	}
	if saved.Status != "ready" {
		return fail(errors.New("Pi read the moved sign-in as not signed in."))
	}
	return importer.PiAccount{ID: saved.ID, Provider: saved.Provider, Kind: saved.Kind}, nil
}

// piLoginEntry holds the fields of a Pi login entry this Dyna release writes.
type piLoginEntry struct {
	Type      string `json:"type"`
	Access    string `json:"access"`
	Refresh   string `json:"refresh"`
	Expires   int64  `json:"expires"`
	AccountID string `json:"accountId"`
	Key       string `json:"key"`
}

// verifyPiLogin reads the account home back and requires exactly the wanted
// entry, so the source is retired only after Pi holds the moved copy.
func verifyPiLogin(home string, login importer.PiLogin) error {
	entries := piSnapshotEntries(home)
	if len(entries) != 1 {
		return fmt.Errorf("The Pi account home holds %d sign-ins, want one.", len(entries))
	}
	saved, ok := entries[login.EntryID]
	if !ok {
		return errors.New("The Pi account home holds another provider's sign-in.")
	}
	var want, got piLoginEntry
	if json.Unmarshal(login.Entry, &want) != nil || json.Unmarshal(saved, &got) != nil || want != got {
		return errors.New("The Pi account home did not keep the moved sign-in.")
	}
	return nil
}

// RemovePiAccount deletes an account that a failed import made.
func (h PiImportHost) RemovePiAccount(_ context.Context, id string) error {
	return h.Runtime.Accounts.Remove(id, true)
}

// SetRepos replaces the repository rules of one account.
func (h PiImportHost) SetRepos(id string, repos []string) error {
	return h.Runtime.Accounts.SetRepos(id, repos)
}

// RetireSource deletes a source sign-in and every row that depends on it. A
// row whose private home is already gone loses its row and rules, so a stale
// home cannot keep a moved sign-in from retiring.
func (h PiImportHost) RetireSource(_ context.Context, id string) error {
	if _, err := h.Runtime.Accounts.Find(id); err != nil {
		return (&store.Store{Root: h.Runtime.Accounts.Root}).Update(func(state *store.State) error {
			state.Remove(id)
			return nil
		})
	}
	return h.Runtime.Accounts.Remove(id, true)
}
