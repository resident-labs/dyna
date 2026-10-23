package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// piProviderSignInID names the one Pi provider sign-in card. Pi keeps every
// provider's credentials in one login file, so a provider sign-in is not an
// account of its own: it adds the provider to Pi's single login.
const piProviderSignInID = "pi:provider"

// beginPiProviderSignIn runs Pi's provider login in a scratch home, then adds
// the new entries to the active account and the runtime home.
func (a *Runtime) beginPiProviderSignIn(exe string) (PrivateLogin, error) {
	if login := a.findPrivateLogin(piProviderSignInID); login != nil && login.signIn != nil && !login.signIn.snapshot().final() {
		state := login.signIn.snapshot()
		return PrivateLogin{AccountID: piProviderSignInID, Harness: "pi", SignIn: &state}, nil
	}
	flow, err := a.piProviderFlow(exe)
	if err != nil {
		return PrivateLogin{}, err
	}
	login, err := a.startPrivateSignIn(piProviderSignInID, "pi", signInSpecs["pi"], flow)
	login.Harness = "pi"
	return login, err
}

func (a *Runtime) piProviderFlow(exe string) (*NativeLogin, error) {
	dir, err := a.Accounts.harnessDirectory("pi")
	if err != nil {
		return nil, err
	}
	id := make([]byte, 8)
	if _, err = rand.Read(id); err != nil {
		return nil, err
	}
	scratch := filepath.Join(dir, ".signin-"+hex.EncodeToString(id))
	if err = safeAccountPath(scratch); err != nil {
		return nil, err
	}
	if err = os.Mkdir(scratch, 0700); err != nil {
		return nil, err
	}
	cleanup := func() {
		_ = os.RemoveAll(scratch)
		_ = os.RemoveAll(scratch + ".home")
	}
	_, overrides, err := a.Accounts.scopedManager("pi", scratch, exe)
	if err == nil {
		err = prepareAccountLogin("pi", scratch, overrides)
	}
	if err != nil {
		cleanup()
		return nil, err
	}
	env := make([]string, 0, len(overrides))
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	var mu sync.Mutex
	finished := false
	var result NativeAccount
	var resultErr error
	return &NativeLogin{Argv: piLoginArgs(exe, scratch), Env: env, Cwd: scratch,
		Complete: func(ctx context.Context) (NativeAccount, error) {
			mu.Lock()
			defer mu.Unlock()
			if finished {
				return result, resultErr
			}
			finished = true
			defer cleanup()
			if resultErr = a.addPiSignIn(ctx, scratch); resultErr == nil {
				result, resultErr = a.Accounts.Active("pi")
			}
			return result, resultErr
		},
		Cancel: func() error {
			mu.Lock()
			defer mu.Unlock()
			if !finished {
				finished = true
				resultErr = errors.New("Native sign-in was cancelled.")
			}
			cleanup()
			return nil
		},
	}, nil
}

// addPiSignIn copies the entries a finished sign-in saved in scratch into the
// active account and the runtime home. A provider it names replaces the old entry.
func (a *Runtime) addPiSignIn(ctx context.Context, scratch string) error {
	entries, err := piEditAuth(ctx, scratch, nil)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return errors.New("Pi did not save a sign-in.")
	}
	return a.addPiEntries(ctx, entries, true)
}

// addPiEntries merges entries into Pi's one login. With replace false an entry
// the login already has stays as it is.
func (a *Runtime) addPiEntries(ctx context.Context, entries map[string]json.RawMessage, replace bool) error {
	home, err := a.Accounts.HarnessHome("pi")
	if err != nil {
		return err
	}
	_, err = a.piEditAccount(ctx, home, func(current map[string]json.RawMessage) {
		for id, entry := range entries {
			if _, have := current[id]; replace || !have {
				current[id] = entry
			}
		}
	})
	return err
}

// piSnapshotEntries reads the provider entries of an account's saved login.
func piSnapshotEntries(home string) map[string]json.RawMessage {
	saved, err := readAccountLogin(home)
	if err != nil {
		return nil
	}
	var files map[string]json.RawMessage
	var entries map[string]json.RawMessage
	if json.Unmarshal(saved.Credential.Payload, &files) != nil || json.Unmarshal(files["auth.json"], &entries) != nil {
		return nil
	}
	return entries
}

// MigratePiAccounts folds every extra Pi account into Pi's one login and removes
// the account, without losing a credential.
// TODO: legacy/shim remove later — Pi provider sign-ins used to create an
// account each. A busy account stays and the next engine start retries it.
func (a *Runtime) MigratePiAccounts(ctx context.Context) {
	accounts, err := a.Accounts.List()
	if err != nil {
		return
	}
	if _, err := a.Accounts.Active("pi"); err != nil {
		return
	}
	for _, n := range accounts {
		if n.Harness != "pi" || n.Active || AccountInUse(n.ID) {
			continue
		}
		entries, err := piEditAuth(ctx, n.Home, nil)
		if err != nil {
			log.Printf("pi accounts: %s was not read: %v", n.ID, err)
			continue
		}
		// The saved snapshot can hold providers the account's own file lacks.
		for id, entry := range piSnapshotEntries(n.Home) {
			if _, have := entries[id]; !have {
				entries[id] = entry
			}
		}
		if err = a.addPiEntries(ctx, entries, false); err != nil {
			log.Printf("pi accounts: %s was not merged: %v", n.ID, err)
			continue
		}
		if err = a.Accounts.Remove(n.ID, true); err != nil {
			log.Printf("pi accounts: %s was not removed: %v", n.ID, err)
		}
	}
}
