package runtime

import (
	"context"
	"errors"

	"dyna-agent/internal/account/store"
)

// AddProviderKey adds an API key as a new account of one provider and makes it
// the active account of that provider. Pi is the harness that takes a key for
// every provider, and each account keeps its own home, so the label the caller
// gives is what the account is called.
func (r *Runtime) AddProviderKey(ctx context.Context, provider store.Provider, label, key string) (NativeAccount, error) {
	piID, ok := PiProviderID(provider)
	if !ok {
		return NativeAccount{}, errors.New("Dyna has no API-key sign-in for this provider.")
	}
	edit, err := piActionEdit("add", piID, key)
	if err != nil {
		return NativeAccount{}, err
	}
	if label == "" {
		label = string(provider) + " key"
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
