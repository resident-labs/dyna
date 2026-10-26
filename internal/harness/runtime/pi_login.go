package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
)

// PiChatGPTLogin returns the access token and ChatGPT account ID of the Pi
// login that may run in repo for a caller in roomID. It reads the login file
// for every search, so a renewal between two searches takes effect at the next
// one.
//
// The web search credential of the engine calls this. When PR #472 lands, its
// renewal script runs at the seam the engine's credential leaves for it, before
// this read.
func (a *Accounts) PiChatGPTLogin(ctx context.Context, roomID, repo string) (access, account string, err error) {
	entry, err := a.piEntry(ctx, store.ProviderChatGPT, roomID, repo)
	if err != nil {
		return "", "", err
	}
	return native.PiOAuthTokens(entry)
}

// PiProviderKey returns the API key that the Pi account of provider which may
// run in repo for a caller in roomID saved in its login file. It serves a
// provider that takes a plain key, such as Brave.
func (a *Accounts) PiProviderKey(ctx context.Context, provider store.Provider, roomID, repo string) (string, error) {
	entry, err := a.piEntry(ctx, provider, roomID, repo)
	if err != nil {
		return "", err
	}
	return native.PiAPIKey(entry)
}

// piEntry reads the login entry of the Pi account that provider uses in roomID
// and that the repo rules allow in repo. The room's own selection wins over the
// global one, and an account the owner limited to other repositories is never
// read, so a search cannot spend an account outside its scope. A Pi account
// holds one provider's entry, and its identity names that provider.
func (a *Accounts) piEntry(ctx context.Context, provider store.Provider, roomID, repo string) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	account, err := a.activeFor("pi", provider, roomID, repo)
	if errors.Is(err, errAccountMissing) {
		return nil, native.ErrNotSignedIn
	}
	if err != nil {
		return nil, err
	}
	id := strings.TrimPrefix(account.Identity, "pi:")
	if id == "" || id == account.Identity {
		return nil, native.ErrNotSignedIn
	}
	entries, err := piEditAuth(ctx, account.Home, nil)
	if err != nil {
		return nil, err
	}
	entry, ok := entries[id]
	if !ok {
		return nil, native.ErrNotSignedIn
	}
	return entry, nil
}
