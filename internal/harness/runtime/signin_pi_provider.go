package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// piProviderSignInID names Pi's provider sign-in card. Pi saves the credentials
// of each provider it signs into, and every provider gets its own account with a
// home that holds only its entry.
const piProviderSignInID = "pi:provider"

// beginPiProviderSignIn runs Pi's provider login in a scratch home, then adds
// the signed-in providers as accounts.
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
			if result, resultErr = a.addPiSignIn(ctx, scratch); resultErr == nil {
				result, resultErr = a.Accounts.Find(result.ID)
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

// addPiSignIn stores each provider a finished sign-in saved in scratch in that
// provider's own account. It returns one of them, for the card that started it.
func (a *Runtime) addPiSignIn(ctx context.Context, scratch string) (NativeAccount, error) {
	entries, err := piEditAuth(ctx, scratch, nil)
	if err != nil {
		return NativeAccount{}, err
	}
	if len(entries) == 0 {
		return NativeAccount{}, errors.New("Pi did not save a sign-in.")
	}
	saved, err := a.savePiProviderEntries(ctx, entries)
	if err != nil {
		return NativeAccount{}, err
	}
	if len(saved) == 0 {
		return NativeAccount{}, errors.New("Pi did not save a usable sign-in.")
	}
	return saved[0], nil
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
