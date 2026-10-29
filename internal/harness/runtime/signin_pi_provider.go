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
	"strings"
	"sync"
	"time"

	accountstore "dyna-agent/internal/account/store"
)

// piProviderSignInID names Pi's provider sign-in card. Pi saves the credentials
// of each provider it signs into, and every provider gets its own account with a
// home that holds only its entry.
const piProviderSignInID = "pi:provider"

// signInScratchPrefix names a provider sign-in's scratch folder under the
// Pi harness directory.
const signInScratchPrefix = ".signin-"

// beginPiProviderSignIn runs Pi's provider login in a scratch home, then adds
// the signed-in providers as accounts. target names the saved account whose
// provider must take the new credential, which a sign-in again passes so a
// second account of that provider is never written by mistake.
func (a *Runtime) beginPiProviderSignIn(exe, target string, provider accountstore.Provider) (PrivateLogin, error) {
	if provider == accountstore.ProviderAnthropic {
		return PrivateLogin{}, ErrPiAnthropicOAuth
	}
	if login := a.findPrivateLogin(piProviderSignInID); login != nil && login.signIn != nil && !login.signIn.snapshot().final() {
		if login.target != target || login.provider != provider {
			return PrivateLogin{}, errors.New("A Pi sign-in is already running for another account or provider. Finish or cancel it first.")
		}
		state := login.signIn.snapshot()
		return PrivateLogin{AccountID: piProviderSignInID, Harness: "pi", Target: login.target, SignIn: &state}, nil
	}
	flow, err := a.piProviderFlow(exe, target, provider)
	if err != nil {
		return PrivateLogin{}, err
	}
	login, err := a.startPrivateSignIn(piProviderSignInID, "pi", signInSpecs["pi"], flow)
	if err != nil {
		return login, err
	}
	a.privateLoginMu.Lock()
	if running := a.privateLogins[piProviderSignInID]; running != nil {
		running.target = target
		running.provider = provider
	}
	a.privateLoginMu.Unlock()
	login.Harness = "pi"
	login.Target = target
	return login, nil
}

func (a *Runtime) piProviderFlow(exe, target string, provider accountstore.Provider) (*NativeLogin, error) {
	providerID := ""
	if provider != "" {
		id, ok := PiOAuthProviderID(provider)
		if !ok {
			return nil, errors.New("This provider has no Pi OAuth sign-in.")
		}
		providerID = id
	}
	dir, err := a.Accounts.harnessDirectory("pi")
	if err != nil {
		return nil, err
	}
	a.sweepStaleSignInScratch(dir)
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
	return &NativeLogin{Argv: piOAuthArgv(exe, providerID), Env: env, Cwd: scratch,
		Complete: func(ctx context.Context) (NativeAccount, error) {
			mu.Lock()
			defer mu.Unlock()
			if finished {
				return result, resultErr
			}
			finished = true
			defer cleanup()
			if result, resultErr = a.addPiSignIn(ctx, scratch, target); resultErr == nil {
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

// sweepStaleSignInScratch removes provider sign-in folders older than the
// sign-in timeout. A flow outlives its login child, and a retained flow's
// folder keeps aging, so age alone never marks a folder unused: folders of
// registered flows are spared, and only their own completion or
// cancellation, which a finish serializes with, removes them.
func (a *Runtime) sweepStaleSignInScratch(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	owned := a.ownedSignInScratch()
	cutoff := time.Now().Add(-signInTimeout)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, signInScratchPrefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.IsDir() || !info.ModTime().Before(cutoff) {
			continue
		}
		for _, path := range []string{filepath.Join(dir, name), filepath.Join(dir, name) + ".home"} {
			if !owned[path] {
				_ = os.RemoveAll(path)
			}
		}
	}
}

// ownedSignInScratch lists the scratch folders that registered sign-in
// flows still hold, so no other cleanup path can take them.
func (a *Runtime) ownedSignInScratch() map[string]bool {
	a.privateLoginMu.Lock()
	defer a.privateLoginMu.Unlock()
	owned := make(map[string]bool, 2*len(a.privateLogins))
	for _, login := range a.privateLogins {
		if login.flow == nil || !strings.HasPrefix(filepath.Base(login.flow.Cwd), signInScratchPrefix) {
			continue
		}
		owned[login.flow.Cwd] = true
		owned[login.flow.Cwd+".home"] = true
	}
	return owned
}

// addPiSignIn stores each provider a finished sign-in saved in scratch in that
// provider's own account. It returns one of them, for the card that started it.
// target is the account a sign-in again must write, or empty for a new account.
func (a *Runtime) addPiSignIn(ctx context.Context, scratch, target string) (NativeAccount, error) {
	entries, err := a.Accounts.piEditAuth(ctx, scratch, nil)
	if err != nil {
		return NativeAccount{}, err
	}
	if len(entries) == 0 {
		return NativeAccount{}, errors.New("Pi did not save a sign-in.")
	}
	saved, err := a.savePiProviderEntries(ctx, entries, target)
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
