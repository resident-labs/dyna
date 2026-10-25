package runtime

import (
	"context"
	"errors"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
	harnesshome "dyna-agent/internal/harness/home"
)

// openCodeKeyIDs maps a provider to the OpenCode integration that keeps its API
// key. Source: store.ProviderFor.
var openCodeKeyIDs = map[store.Provider]string{
	store.ProviderOpenCodeGo: "opencode-go",
	store.ProviderAnthropic:  "anthropic",
	store.ProviderChatGPT:    "openai",
	store.ProviderXAI:        "xai",
	store.ProviderDeepSeek:   "deepseek",
	store.ProviderZAI:        "zai",
}

// AddProviderKey adds an API key as a new account of one provider and makes it
// the active account of that provider. Each account keeps its own home, so the
// label the caller gives is what the account is called.
//
// harness names where the key lives: Pi takes a key for every provider, and
// OpenCode keeps its own providers' keys in the account home an OpenCode
// profile runs. An empty harness means Pi.
func (r *Runtime) AddProviderKey(ctx context.Context, harness string, provider store.Provider, label, key string) (NativeAccount, error) {
	switch harness {
	case "", "pi":
		return r.addPiProviderKey(ctx, provider, label, key)
	case "opencode":
		return r.addOpenCodeProviderKey(ctx, provider, label, key)
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

// addOpenCodeProviderKey imports a key into OpenCode's own store and records
// the account that owns it, which is what an OpenCode profile runs. OpenCode
// keeps its credentials in one service store, so the account is adopted from
// the captured credential: the importer needs that store, and a device login
// needs a terminal.
func (r *Runtime) addOpenCodeProviderKey(ctx context.Context, provider store.Provider, label, key string) (NativeAccount, error) {
	id, ok := openCodeKeyIDs[provider]
	if !ok {
		return NativeAccount{}, errors.New("OpenCode has no API key for this provider.")
	}
	bundle, err := r.Bundles.Resolve("opencode", "")
	if err != nil {
		return NativeAccount{}, errors.New("Install Dyna's copy of OpenCode first.")
	}
	home, err := r.Accounts.HarnessHome("opencode")
	if err != nil {
		return NativeAccount{}, err
	}
	if err = harnesshome.Ensure(home); err != nil {
		return NativeAccount{}, err
	}
	manager, _, err := r.Accounts.scopedManager("opencode", home, bundle.Executable)
	if err != nil {
		return NativeAccount{}, err
	}
	if checked, ok := manager.(checkedHomeManager); ok {
		manager = checked.Manager
	}
	keys, ok := native.OpenCodeKeysOf(manager)
	if !ok {
		return NativeAccount{}, errProviderKeys
	}
	known, err := r.Accounts.List()
	if err != nil {
		return NativeAccount{}, err
	}
	if err = keys.Add(ctx, id, key); err != nil {
		return NativeAccount{}, err
	}
	rows, err := manager.Capture(ctx, "")
	if err != nil && !errors.Is(err, native.ErrNotSignedIn) {
		return NativeAccount{}, err
	}
	var chosen *native.Captured
	for i := range rows {
		c := rows[i]
		if c.Integration != id || c.Identity == "" || identitySaved(known, c.Identity) {
			continue
		}
		chosen = &c
		break
	}
	if chosen == nil {
		return NativeAccount{}, errors.New("OpenCode did not save the API key.")
	}
	if label == "" {
		label = string(provider) + " key"
	}
	err = r.Accounts.transaction(func(state *store.State) error {
		if err := r.Accounts.saveOutgoing(state, "opencode", *chosen); err != nil {
			return err
		}
		for i := range state.Entries {
			e := &state.Entries[i]
			if e.Harness == "opencode" && e.Identity == chosen.Identity {
				e.Label = label
			}
		}
		return nil
	}, true)
	if err != nil {
		return NativeAccount{}, err
	}
	var account *NativeAccount
	accounts, err := r.Accounts.List()
	if err != nil {
		return NativeAccount{}, err
	}
	for i := range accounts {
		if accounts[i].Identity == chosen.Identity {
			account = &accounts[i]
			break
		}
	}
	if account == nil {
		return NativeAccount{}, errors.New("OpenCode did not save a usable account.")
	}
	if err := r.Accounts.setActiveAccount(*account); err != nil {
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
