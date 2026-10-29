package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"slices"
	"time"

	"dyna-agent/internal/account/store"
)

// Pi keeps every provider credential in one login file, so a Pi account used to
// hold several providers at once. This release gives each provider its own
// account, each with a home that holds only that provider's entry, so two Pi
// turns of different providers run side by side. The shared login file is left
// in place until no turn started from it can run.

// piEntryKind names how a Pi login entry signed in.
func piEntryKind(entry json.RawMessage) store.Kind {
	var kind struct{ Type string }
	if json.Unmarshal(entry, &kind) == nil && kind.Type == "api_key" {
		return store.KindAPIKey
	}
	return store.KindOAuth
}

// writePiAccountAuth replaces a Pi account's login with exactly the entries
// given, through Pi's own auth commands. A provider that is not named leaves the file.
func (r *Runtime) writePiAccountAuth(ctx context.Context, account NativeAccount, entries map[string]json.RawMessage) error {
	piKeysMu.Lock()
	defer piKeysMu.Unlock()
	_, err := r.Accounts.piEditAuth(ctx, account.Home, func(current map[string]json.RawMessage) {
		clear(current)
		for id, entry := range entries {
			current[id] = entry
		}
	})
	return err
}

// piAccountHolds reports whether an account home already holds the entry of id.
func (a *Accounts) piAccountHolds(account NativeAccount, id string) bool {
	_, held := a.piSavedEntries(context.Background(), account.Home)[id]
	return held
}

// piAccountForProvider returns the Pi account that provider uses: the selected
// one when it holds that provider, else any ready account of it.
func (a *Runtime) piAccountForProvider(ctx context.Context, provider store.Provider) (NativeAccount, bool, error) {
	accounts, err := a.Accounts.List()
	if err != nil {
		return NativeAccount{}, false, err
	}
	active, _ := (&store.Store{Root: a.Accounts.Root}).Active(provider, "pi", "")
	var ready *NativeAccount
	for i := range accounts {
		n := accounts[i]
		if n.Harness != "pi" || n.Provider != provider {
			continue
		}
		if n.ID == active {
			return n, true, nil
		}
		if ready == nil && n.Status == "ready" {
			ready = &accounts[i]
		}
	}
	if ready != nil {
		return *ready, true, nil
	}
	return NativeAccount{}, false, nil
}

// savePiProviderEntries stores each Pi provider entry in the account of that
// provider, creates the account when the provider has none, records its login,
// and makes it the active account of the provider. It returns the accounts it
// saved, in provider order.
//
// target names an account that must take the entry of its own provider, as a
// sign-in again does: that account may not be the provider's active one, and
// the selection must not change, because the owner asked to refresh one saved
// credential, not to switch providers.
func (a *Runtime) savePiProviderEntries(ctx context.Context, entries map[string]json.RawMessage, target string) ([]NativeAccount, error) {
	if target != "" {
		// A sign-in again names the one account it may write. A target that is
		// gone, or that cannot take a Pi credential, must never fall through to
		// the generic path: that would write whichever account of a provider is
		// active, which is the account the owner did not ask to change.
		n, err := a.Accounts.Find(target)
		if err != nil {
			return nil, errors.New("The account this sign-in was started for is gone. Start the sign-in again from the account list.")
		}
		if n.Harness != "pi" || n.Provider == "" {
			return nil, errors.New("The account this sign-in was started for cannot take a Pi credential.")
		}
		return a.saveTargetedPiEntry(ctx, entries, n)
	}
	var saved []NativeAccount
	var firstErr error
	for _, id := range slices.Sorted(maps.Keys(entries)) {
		provider := piProvider(id)
		account, found, err := a.piAccountForProvider(ctx, provider)
		if err != nil {
			firstErr = errors.Join(firstErr, err)
			continue
		}
		if !found {
			account, err = a.Accounts.CreateProvider("pi", provider, piEntryKind(entries[id]), id+" login")
			if err != nil {
				firstErr = errors.Join(firstErr, err)
				continue
			}
		}
		if err := a.writePiAccountAuth(ctx, account, map[string]json.RawMessage{id: entries[id]}); err != nil {
			firstErr = errors.Join(firstErr, err)
			continue
		}
		captured, err := a.Accounts.Capture(ctx, account.ID, "")
		if err != nil {
			firstErr = errors.Join(firstErr, err)
			continue
		}
		if err := a.Accounts.setActiveAccount(captured); err != nil {
			firstErr = errors.Join(firstErr, err)
			continue
		}
		saved = append(saved, captured)
	}
	return saved, firstErr
}

// saveTargetedPiEntry writes only the target account's provider entry. A
// sign-in again must change that account and nothing else, and the provider
// card offers every provider: a credential for another provider is refused,
// because writing it would replace that provider's active account, which the
// owner did not ask for.
func (a *Runtime) saveTargetedPiEntry(ctx context.Context, entries map[string]json.RawMessage, target NativeAccount) ([]NativeAccount, error) {
	var entryID string
	var entry json.RawMessage
	for _, id := range slices.Sorted(maps.Keys(entries)) {
		if piProvider(id) == target.Provider {
			entryID, entry = id, entries[id]
			break
		}
	}
	if entry == nil {
		return nil, fmt.Errorf("This sign-in saved no %s credential. Sign in again and choose %s.", target.Provider, target.Provider)
	}
	if err := a.writePiAccountAuth(ctx, target, map[string]json.RawMessage{entryID: entry}); err != nil {
		return nil, err
	}
	captured, err := a.Accounts.Capture(ctx, target.ID, "")
	if err != nil {
		return nil, err
	}
	return []NativeAccount{captured}, nil
}

// adoptProviderAccount gives a Pi row that held several providers the provider
// it keeps from now on. Its home stays, so its history stays with it.
func (a *Accounts) adoptProviderAccount(n NativeAccount, provider store.Provider, kind store.Kind, label string) (NativeAccount, error) {
	err := a.transaction(func(state *store.State) error {
		e := state.Find(n.ID)
		if e == nil {
			return errAccountMissing
		}
		e.Provider, e.Kind, e.Label = provider, kind, label
		e.NeedsLogin = false
		e.UpdatedAt = time.Now().UTC()
		return nil
	}, true)
	if err != nil {
		return NativeAccount{}, err
	}
	return a.Find(n.ID)
}

// SplitPiAccounts moves each provider credential of Pi's shared login into an
// account of that provider. It runs at every start until the step is deleted,
// and it is resumable: the plan and the account of every moved entry live in
// dyna.db, so a start that stopped halfway retries each unfinished step and
// never moves an entry twice.
//
// TODO: legacy/shim remove later — drop this step, its tables and the shared
// login file once every Pi turn launches from an account home.
func (r *Runtime) SplitPiAccounts(ctx context.Context) {
	db := &store.Store{Root: r.Accounts.Root}
	sources, err := db.PiSplitSources(ctx)
	if err != nil {
		log.Printf("pi accounts: the split plan could not be read: %v", err)
		return
	}
	if len(sources) == 0 {
		if sources, err = r.planPiSplit(ctx); err != nil {
			log.Printf("pi accounts: the shared login could not be read: %v", err)
			return
		}
		for _, source := range sources {
			if err := db.SavePiSplitSource(ctx, source); err != nil {
				log.Printf("pi accounts: the split plan could not be saved: %v", err)
				return
			}
		}
		if len(sources) == 0 {
			return
		}
	}
	steps, err := db.PiSplitDone(ctx)
	if err != nil {
		log.Printf("pi accounts: the split progress could not be read: %v", err)
		return
	}
	shared, err := r.Accounts.HarnessHome("pi")
	if err != nil {
		return
	}
	sharedEntries, err := r.Accounts.piEditAuth(ctx, shared, nil)
	if err != nil {
		log.Printf("pi accounts: the shared login could not be read: %v", err)
		return
	}
	// A credential added to the shared login after the plan was saved joins the plan.
	if sources, err = r.addSharedEntries(ctx, db, sources, sharedEntries, shared); err != nil {
		log.Printf("pi accounts: the split plan could not be extended: %v", err)
		return
	}
	activeLegacy, _ := db.Active("", "pi", "")
	var firstErr error
	for _, source := range sources {
		if err := r.splitPiSource(ctx, source, sharedEntries, steps, activeLegacy); err != nil {
			firstErr = errors.Join(firstErr, err)
		}
	}
	if firstErr != nil {
		log.Printf("pi accounts: the split of Pi's shared login is incomplete and runs again at the next start: %v", firstErr)
	}
}

// planPiSplit reads the plan of the split: every Pi account that still holds
// several providers, the entries it holds and the repositories it may run in.
func (r *Runtime) planPiSplit(ctx context.Context) ([]store.PiSplitSource, error) {
	accounts, err := r.Accounts.List()
	if err != nil {
		return nil, err
	}
	shared, err := r.Accounts.HarnessHome("pi")
	if err != nil {
		return nil, err
	}
	sharedEntries, err := r.Accounts.piEditAuth(ctx, shared, nil)
	if err != nil {
		return nil, err
	}
	var sources []store.PiSplitSource
	owned := map[string]bool{}
	for _, n := range accounts {
		if n.Harness != "pi" || n.Provider != "" {
			continue
		}
		entries := map[string]json.RawMessage{}
		for id, entry := range r.Accounts.piSavedEntries(ctx, n.Home) {
			entries[id] = entry
			owned[id] = true
		}
		for id, entry := range piSnapshotEntries(n.Home) {
			if _, held := entries[id]; !held {
				entries[id] = entry
				owned[id] = true
			}
		}
		if len(entries) == 0 {
			continue
		}
		sources = append(sources, store.PiSplitSource{Account: n.ID, Home: n.Home, Label: n.Label,
			Repos: slices.Clone(n.Repos), Entries: slices.Sorted(maps.Keys(entries))})
	}
	selected, _ := (&store.Store{Root: r.Accounts.Root}).Active("", "pi", "")
	return assignSharedEntries(sources, sharedEntries, owned, selected, shared), nil
}

// assignSharedEntries gives each shared-login entry that no source holds to a
// source. An entry that a source account also holds belongs to that account and
// moves under its rules. An entry only the shared login holds ran under the
// account that was selected for Pi, so it keeps that account's limits; giving
// it another account's rules, or no rules at all, would let a credential run
// where its account was limited. With one account, the shared login was that
// account's login, so all of it moves under its rules.
func assignSharedEntries(sources []store.PiSplitSource, sharedEntries map[string]json.RawMessage, owned map[string]bool, selected, shared string) []store.PiSplitSource {
	var owner *store.PiSplitSource
	for i := range sources {
		if len(sources) == 1 || sources[i].Account == selected {
			owner = &sources[i]
			break
		}
	}
	var orphan []string
	for id := range sharedEntries {
		if owned[id] {
			continue
		}
		if owner != nil {
			owner.Entries = append(owner.Entries, id)
			owned[id] = true
			continue
		}
		orphan = append(orphan, id)
	}
	if owner != nil {
		slices.Sort(owner.Entries)
	}
	if len(orphan) > 0 {
		// No account was selected, so no account's limits are known for these.
		log.Printf("pi accounts: %d shared credentials have no selected account and move without repository limits", len(orphan))
		slices.Sort(orphan)
		for i := range sources {
			if sources[i].Account == "" {
				sources[i].Entries = slices.Sorted(slices.Values(append(sources[i].Entries, orphan...)))
				return sources
			}
		}
		sources = append(sources, store.PiSplitSource{Account: "", Home: shared, Label: "shared login", Entries: orphan})
	}
	return sources
}

// splitPiSource moves every provider entry of one source. The source row itself
// is kept for one provider, and that provider is moved last, so the row still
// holds the other entries in its home while they are moved.
func (r *Runtime) splitPiSource(ctx context.Context, source store.PiSplitSource, shared map[string]json.RawMessage, steps map[store.PiSplitKey]store.PiSplitStep, activeLegacy string) error {
	db := &store.Store{Root: r.Accounts.Root}
	var pending []string
	for _, id := range source.Entries {
		key := store.PiSplitKey{Source: source.Account, Provider: id}
		entry := r.piSplitEntry(ctx, source.Home, shared, id)
		if entry == nil {
			if err := db.ClearPiSplitDone(ctx, key); err != nil {
				return err
			}
			continue
		}
		step, moved := steps[key]
		if !moved {
			pending = append(pending, id)
			continue
		}
		// A completed entry is never touched again, so an owner who restricts the
		// account afterwards keeps that change. An unfinished one retries every
		// step, because a start can have stopped after any of them.
		if step.Completed {
			continue
		}
		account, err := r.Accounts.Find(step.Account)
		if err != nil {
			// The owner removed this account after the split, so it stays gone.
			if err := db.CompletePiSplit(ctx, key); err != nil {
				return err
			}
			continue
		}
		if err := r.finishPiSplitEntry(ctx, source, account, id, entry, activeLegacy); err != nil {
			return err
		}
		if err := db.CompletePiSplit(ctx, key); err != nil {
			return err
		}
	}
	var adopt *NativeAccount
	if n, err := r.Accounts.Find(source.Account); err == nil && n.Provider == "" {
		adopt = &n
	}
	for i, id := range pending {
		key := store.PiSplitKey{Source: source.Account, Provider: id}
		entry := r.piSplitEntry(ctx, source.Home, shared, id)
		if entry == nil {
			continue
		}
		provider := piProvider(id)
		var account NativeAccount
		var err error
		if adopt != nil && i == len(pending)-1 {
			account, err = r.Accounts.adoptProviderAccount(*adopt, provider, piEntryKind(entry), source.Label)
			adopt = nil
		} else {
			account, err = r.Accounts.CreateProvider("pi", provider, piEntryKind(entry), source.Label)
		}
		if err != nil {
			return err
		}
		// The plan names the account before its entry is written, so a start that
		// stops now finishes this entry instead of making a second account.
		if err := db.MarkPiSplitDone(ctx, key, account.ID); err != nil {
			return err
		}
		if err := r.finishPiSplitEntry(ctx, source, account, id, entry, activeLegacy); err != nil {
			return err
		}
		if err := db.CompletePiSplit(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

// finishPiSplitEntry writes one provider entry into an account, gives it the
// source account's repository rules before it can be ready, records its login,
// and makes it active when the source was the selected account or the provider
// has no selection yet. Every step is safe to repeat.
func (r *Runtime) finishPiSplitEntry(ctx context.Context, source store.PiSplitSource, account NativeAccount, id string, entry json.RawMessage, activeLegacy string) error {
	if err := r.writePiAccountAuth(ctx, account, map[string]json.RawMessage{id: entry}); err != nil {
		return err
	}
	if err := r.Accounts.SetRepos(account.ID, source.Repos); err != nil {
		return err
	}
	captured, err := r.Accounts.Capture(ctx, account.ID, "")
	if err != nil {
		return err
	}
	selected, _ := (&store.Store{Root: r.Accounts.Root}).Active(piProvider(id), "pi", "")
	if selected != "" && source.Account != activeLegacy {
		return nil
	}
	return r.Accounts.setActiveAccount(captured)
}

// piSplitEntry returns the credential a source holds for entry id, from its own
// login first: that entry belongs to that account and its repository rules. Only
// a provider the source's own home lacks comes from the shared login it used to
// run from.
func (r *Runtime) piSplitEntry(ctx context.Context, home string, shared map[string]json.RawMessage, id string) json.RawMessage {
	if entry, held := r.Accounts.piSavedEntries(ctx, home)[id]; held {
		return entry
	}
	if entry, held := piSnapshotEntries(home)[id]; held {
		return entry
	}
	if entry, held := shared[id]; held {
		return entry
	}
	return nil
}

// piSavedEntries reads the provider entries an account home still holds.
func (a *Accounts) piSavedEntries(ctx context.Context, home string) map[string]json.RawMessage {
	entries, err := a.piEditAuth(ctx, home, nil)
	if err == nil {
		return entries
	}
	return piSnapshotEntries(home)
}

// addSharedEntries adds the shared-login entries that the saved plan lacks to
// the plan, and saves each source it changed.
func (r *Runtime) addSharedEntries(ctx context.Context, db *store.Store, sources []store.PiSplitSource, sharedEntries map[string]json.RawMessage, shared string) ([]store.PiSplitSource, error) {
	owned := map[string]bool{}
	before := map[string]int{}
	for _, source := range sources {
		before[source.Account] = len(source.Entries)
		for _, id := range source.Entries {
			owned[id] = true
		}
	}
	selected, _ := db.Active("", "pi", "")
	sources = assignSharedEntries(sources, sharedEntries, owned, selected, shared)
	for _, source := range sources {
		if n, planned := before[source.Account]; planned && n == len(source.Entries) {
			continue
		}
		if err := db.SavePiSplitSource(ctx, source); err != nil {
			return sources, err
		}
	}
	return sources, nil
}
