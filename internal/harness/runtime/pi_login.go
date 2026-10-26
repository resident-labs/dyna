package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
)

// ChatGPTGrant is the access token and account ID of one ChatGPT sign-in. A
// caller must never log or store it.
type ChatGPTGrant struct {
	Access  string
	Account string
}

// ErrGrantExpired says the ChatGPT sign-in is due and could not be renewed, so
// the owner must sign in again.
var ErrGrantExpired = errors.New("the ChatGPT sign-in expired and could not be renewed")

// ErrPiMissing says Dyna's own copy of Pi is not installed, and Pi is what
// renews a saved grant.
var ErrPiMissing = errors.New("Dyna's copy of Pi is not installed")

// renewWindow is how long before its expiry a grant is renewed, so a request
// never starts with a token that dies while it runs.
const renewWindow = 5 * time.Minute

// ChatGPTGrant returns the ChatGPT sign-in that may run in repo for a caller in
// roomID, renewed first when it is due.
//
// It is the one reader of that sign-in: the voice service and the web search
// tool both call it, so account selection, token validation and renewal live
// here. Each caller passes its own room and repository scope, and an account the
// owner limited to other repositories is never read. A caller must never log the
// grant.
func (a *Accounts) ChatGPTGrant(ctx context.Context, roomID, repo string) (ChatGPTGrant, error) {
	id, entry, err := a.piLogin(ctx, store.ProviderChatGPT, roomID, repo)
	if err != nil {
		return ChatGPTGrant{}, err
	}
	due, err := grantDue(entry)
	if err != nil {
		return ChatGPTGrant{}, err
	}
	if due {
		executable, err := a.piExecutable()
		if err != nil {
			return ChatGPTGrant{}, fmt.Errorf("%w: %w", ErrGrantExpired, err)
		}
		if _, err := a.RefreshCredentials(ctx, id, executable); err != nil {
			return ChatGPTGrant{}, fmt.Errorf("%w: %w", ErrGrantExpired, err)
		}
		if _, entry, err = a.piLogin(ctx, store.ProviderChatGPT, roomID, repo); err != nil {
			return ChatGPTGrant{}, err
		}
	}
	access, account, err := native.PiOAuthTokens(entry)
	if err != nil {
		return ChatGPTGrant{}, err
	}
	return ChatGPTGrant{Access: access, Account: account}, nil
}

// grantDue reports a grant that expires inside renewWindow. A grant with no
// expiry never counts as due.
func grantDue(entry json.RawMessage) (bool, error) {
	expires, err := native.PiGrantExpiry(entry)
	if err != nil {
		return false, err
	}
	return !expires.IsZero() && time.Until(expires) < renewWindow, nil
}

// piExecutable is Dyna's own copy of Pi, which renews a grant with Pi's own
// refresh hook. A saved login is renewed under Pi's lock by that copy, never by
// whichever Pi the owner installed.
func (a *Accounts) piExecutable() (string, error) {
	if a.PiExecutable != nil {
		return a.PiExecutable()
	}
	bundle, err := NewBundleManager().Resolve("pi", "")
	if err != nil {
		return "", ErrPiMissing
	}
	return bundle.Executable, nil
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

// PiInferenceKey returns the key a Pi login in repo may call its provider's
// API with, in whichever shape Pi saved it: the access token of an OAuth
// sign-in or a pasted API key. It serves a provider that accepts both, such as
// Meta Muse, whose OAuth sign-in keeps the minted inference key in access.
func (a *Accounts) PiInferenceKey(ctx context.Context, provider store.Provider, roomID, repo string) (string, error) {
	entry, err := a.piEntry(ctx, provider, roomID, repo)
	if err != nil {
		return "", err
	}
	return native.PiOAuthOrAPIKey(entry)
}

// piEntry reads the login entry of the Pi account that provider uses in roomID
// and that the repo rules allow in repo. The room's own selection wins over the
// global one, and an account the owner limited to other repositories is never
// read, so a search cannot spend an account outside its scope. A Pi account
// holds one provider's entry, and its identity names that provider.
func (a *Accounts) piEntry(ctx context.Context, provider store.Provider, roomID, repo string) (json.RawMessage, error) {
	_, entry, err := a.piLogin(ctx, provider, roomID, repo)
	return entry, err
}

// piLogin reads the account ID and the login entry of the Pi account that
// provider uses in roomID and that the repo rules allow in repo. The ID is the
// account's, which a caller needs to renew its grant.
func (a *Accounts) piLogin(ctx context.Context, provider store.Provider, roomID, repo string) (string, json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	account, err := a.activeFor("pi", provider, roomID, repo)
	if errors.Is(err, errAccountMissing) {
		return "", nil, native.ErrNotSignedIn
	}
	if err != nil {
		return "", nil, err
	}
	id := strings.TrimPrefix(account.Identity, "pi:")
	if id == "" || id == account.Identity {
		return "", nil, native.ErrNotSignedIn
	}
	entries, err := piEditAuth(ctx, account.Home, nil)
	if err != nil {
		return "", nil, err
	}
	entry, ok := entries[id]
	if !ok {
		return "", nil, native.ErrNotSignedIn
	}
	return account.ID, entry, nil
}

// PiAccessToken returns the access token of the Pi login that may run in repo
// for a caller in roomID. Unlike ChatGPTGrant it needs no account ID, so it
// serves a provider that signs in without one, such as xAI or Kimi Code.
func (a *Accounts) PiAccessToken(ctx context.Context, provider store.Provider, roomID, repo string) (string, error) {
	entry, err := a.piEntry(ctx, provider, roomID, repo)
	if err != nil {
		return "", err
	}
	return native.PiAccessToken(entry)
}
