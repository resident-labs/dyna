package runtime

import (
	"context"
	"encoding/json"
	"strings"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
)

// PiChatGPTLogin returns the access token and ChatGPT account ID of the active
// Pi login for ChatGPT. It reads the login file for every search, so a renewal
// between two searches takes effect at the next one.
//
// The web search credential of the engine calls this. When PR #472 lands, its
// renewal script runs at the seam the engine's credential leaves for it, before
// this read.
func (a *Accounts) PiChatGPTLogin(ctx context.Context) (access, account string, err error) {
	entry, err := a.piEntry(ctx, store.ProviderChatGPT)
	if err != nil {
		return "", "", err
	}
	return native.PiOAuthTokens(entry)
}

// PiProviderKey returns the API key that the active Pi account of provider
// saved in its login file. It serves a provider that takes a plain key, such as
// Brave.
func (a *Accounts) PiProviderKey(ctx context.Context, provider store.Provider) (string, error) {
	entry, err := a.piEntry(ctx, provider)
	if err != nil {
		return "", err
	}
	return native.PiAPIKey(entry)
}

// piEntry reads the login entry of the active Pi account for provider. A Pi
// account holds one provider's entry, and its identity names that provider.
func (a *Accounts) piEntry(ctx context.Context, provider store.Provider) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	accounts, err := a.List()
	if err != nil {
		return nil, err
	}
	for _, account := range accounts {
		if account.Harness != "pi" || account.Provider != provider || !account.Active {
			continue
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
	return nil, native.ErrNotSignedIn
}
