package runtime

import (
	"context"
	"dyna-agent/internal/account/native"
	"errors"

	"dyna-agent/internal/account/store"
)

// AddProviderKey adds an API key as a new account of one provider and makes it
// the active account of that provider. Each account keeps its own home, so the
// label the caller gives is what the account is called.
//
// An empty harness means Pi, which takes a key for every provider.
func (r *Runtime) AddProviderKey(ctx context.Context, harness string, provider store.Provider, label, key string) (NativeAccount, error) {
	switch harness {
	case "", "pi":
		return r.addPiProviderKey(ctx, provider, label, key)
	default:
		return NativeAccount{}, errors.New("This harness does not take provider API keys.")
	}
}

// addPiProviderKey keeps a key in a Pi account's own home.
func (r *Runtime) addPiProviderKey(ctx context.Context, provider store.Provider, label, key string) (NativeAccount, error) {
	piID, ok := PiProviderID(provider)
	if !ok {
		return NativeAccount{}, errors.New("Dyna has no API-key sign-in for this provider.")
	}
	edit := piAPIKeyEdit(piID, key)
	if label == "" {
		label = native.PiKeyLabel(string(provider), key)
	}
	piKeysMu.Lock()
	defer piKeysMu.Unlock()
	release, err := r.lockPiAccounts(ctx)
	if err != nil {
		return NativeAccount{}, err
	}
	defer release()
	account, err := r.Accounts.CreateProvider("pi", provider, store.KindAPIKey, label)
	if err != nil {
		return NativeAccount{}, err
	}
	if err := r.writePiAccountLogin(ctx, account, edit); err != nil {
		return NativeAccount{}, err
	}
	return r.Accounts.Find(account.ID)
}

// identitySaved reports whether an account already holds this native identity,
// so a new key is never mistaken for an account the owner already has.
func identitySaved(accounts []NativeAccount, identity string) bool {
	for _, n := range accounts {
		if n.Identity == identity {
			return true
		}
	}
	return false
}
