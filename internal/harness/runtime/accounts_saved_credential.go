package runtime

import (
	"context"
	"errors"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
)

// SavedCredential returns the current credential of the account of one
// provider that may run in repo for a caller in roomID, on a harness that keeps
// its own credential, such as Claude Code. A Pi account keeps its login in its
// own home instead, so a Pi credential reads through PiAccessToken or
// PiProviderKey.
//
// The engine's web search tool reads it for every search, so a switch of the
// active account takes effect at the next search, and an account the owner
// limited to other repositories is never read. A caller must never log the
// credential.
func (a *Accounts) SavedCredential(ctx context.Context, provider store.Provider, roomID, repo string) (store.Credential, error) {
	if err := ctx.Err(); err != nil {
		return store.Credential{}, err
	}
	accounts, err := a.List()
	if err != nil {
		return store.Credential{}, err
	}
	harness := ""
	for _, account := range accounts {
		if account.Harness != "pi" && account.Provider == provider {
			harness = account.Harness
			break
		}
	}
	if harness == "" {
		return store.Credential{}, native.ErrNotSignedIn
	}
	chosen, err := a.activeFor(harness, provider, roomID, repo)
	if errors.Is(err, errAccountMissing) {
		return store.Credential{}, native.ErrNotSignedIn
	}
	if err != nil {
		return store.Credential{}, err
	}
	// A running turn rotates the grant in the account's own credentials folder,
	// so the saved snapshot can be behind it. Read the folder the account's
	// turns read, which is the current credential source.
	manager, err := a.launchManager(chosen.ID, "")
	if err != nil {
		return store.Credential{}, err
	}
	logins, err := manager.Capture(ctx, "")
	if err != nil {
		return store.Credential{}, err
	}
	if len(logins) == 0 {
		return store.Credential{}, native.ErrNotSignedIn
	}
	for _, captured := range logins {
		if captured.Identity == chosen.Identity {
			return captured.Credential, nil
		}
	}
	return store.Credential{}, native.ErrNoIdentity
}
