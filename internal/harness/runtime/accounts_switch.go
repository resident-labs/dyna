package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
)

// accountProvider maps a harness to the provider its saved accounts belong to.
// The active table is keyed by provider, and this release still asks for the
// active account by harness. A later PR passes the provider from the profile
// instead.
func accountProvider(harness string) store.Provider {
	provider, _ := store.ProviderFor(harness, "")
	return provider
}

// activeID is the account that harness h uses, from the account registry.
func (a *Accounts) activeID(h string) (string, error) {
	return (&store.Store{Root: a.Root}).Active(accountProvider(h), h, "")
}

// setActive records the account that harness h uses, in the registry.
func (a *Accounts) setActive(h, id string) error {
	return (&store.Store{Root: a.Root}).SetActive(accountProvider(h), h, "", id)
}

// setActiveAccount records that harness n.Harness uses n for its provider. Each
// harness keeps its own selection, so two harnesses that run one provider never
// take each other's account.
func (a *Accounts) setActiveAccount(n NativeAccount) error {
	return (&store.Store{Root: a.Root}).SetActive(SelectionProvider(n.Harness, n.Provider), n.Harness, "", n.ID)
}

// clearActive records that the account's harness has no selection for its
// provider.
func (a *Accounts) clearActive(n NativeAccount) error {
	return (&store.Store{Root: a.Root}).SetActive(SelectionProvider(n.Harness, n.Provider), n.Harness, "", "")
}

// selectPinned makes an account the active one of its provider without copying
// any credential: the harness reads that account's own home. Pi keeps one
// provider login per account, so its turns need no swap and no drain.
func (a *Accounts) selectPinned(n NativeAccount) error {
	if n.Status != "ready" {
		return native.ErrNoIdentity
	}
	return a.setActiveAccount(n)
}

// activeAccount returns the account that harness h runs for provider p in
// roomID. The room's own selection wins over the global one, and each harness
// keeps its own selection: two harnesses that run one provider, such as Claude
// Code and Pi for anthropic, never take each other's account away.
func (a *Accounts) activeAccount(h string, p store.Provider, roomID string) (NativeAccount, error) {
	canonical, err := canonicalAccountHarness(h)
	if err != nil {
		return NativeAccount{}, err
	}
	p = SelectionProvider(canonical, p)
	id, err := (&store.Store{Root: a.Root}).ActiveFor(p, canonical, roomID)
	if err != nil {
		return NativeAccount{}, err
	}
	if id == "" {
		return NativeAccount{}, errAccountMissing
	}
	n, err := a.Find(id)
	if err != nil {
		return n, err
	}
	if !accountRuns(canonical, n, p) {
		if canonical == "pi" && p == store.ProviderAnthropic && n.Provider == p && n.Kind != store.KindAPIKey {
			return NativeAccount{}, ErrPiAnthropicOAuth
		}
		return NativeAccount{}, errAccountMissing
	}
	return n, nil
}

// Active returns the account its selection provider uses for harness h. A
// harness that keeps one selection per provider, such as Pi, needs the
// provider: an empty provider would find no account. A caller that has an
// account's credential provider must pass SelectionProvider(h, provider): a
// non-Pi harness keeps one selection key, so a keyed OpenCode account can be
// selected under opencode-go while its credential names another provider.
func (a *Accounts) Active(provider store.Provider, harness string) (NativeAccount, error) {
	h, err := canonicalAccountHarness(harness)
	if err != nil {
		return NativeAccount{}, err
	}
	id, err := (&store.Store{Root: a.Root}).Active(provider, h, "")
	if err != nil {
		return NativeAccount{}, err
	}
	if id == "" {
		return NativeAccount{}, errAccountMissing
	}
	n, err := a.Find(id)
	if err == nil && n.Harness != h {
		return NativeAccount{}, errAccountPath
	}
	return n, err
}

func (a *Accounts) harnessDirectory(harness string) (string, error) {
	h, err := canonicalAccountHarness(harness)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(a.Root, h)
	if err = safeAccountPath(dir); err != nil {
		return "", err
	}
	for _, path := range []string{a.Root, dir, filepath.Join(dir, ".turns")} {
		if err = os.MkdirAll(path, 0700); err != nil {
			return "", err
		}
		if err = safeAccountPath(path); err != nil {
			return "", err
		}
		if err = os.Chmod(path, 0700); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func openAccountLock(path string, create bool) (*os.File, error) {
	if err := safeAccountPath(path); err != nil {
		return nil, err
	}
	flags := os.O_RDWR | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if create {
		flags |= os.O_CREATE
	}
	f, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, errAccountPath
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		f.Close()
		return nil, errAccountPath
	}
	return f, nil
}

func acquireAccountLock(ctx context.Context, path string, kind int) (*os.File, error) {
	f, err := openAccountLock(path, true)
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err = ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		err = syscall.Flock(int(f.Fd()), kind|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
func releaseAccountLock(f *os.File) {
	if f != nil {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}
}

// pinsPerAccount reports whether each account of harness h keeps its
// credentials in its own home. Such a harness needs no drain and no shared
// credential swap: a turn reads the home of the account it started on.
func pinsPerAccount(h string) bool { return pinsCredentials(h) || h == "pi" }

// TurnLease protects one native process lifetime, including its retries.
// The admission gate prevents new shared turns from bypassing a waiting switch.
func (a *Accounts) TurnLease(ctx context.Context, harness string) (func(), error) {
	lease, err := a.TurnLeaseManaged(ctx, harness)
	if err != nil {
		return nil, err
	}
	return func() { _ = lease.Close() }, nil
}

// TurnLeaseManaged adds crash-safe native process protection to the turn gate.
func (a *Accounts) TurnLeaseManaged(ctx context.Context, harness string) (*ResourceLease, error) {
	h, err := canonicalAccountHarness(harness)
	if err != nil {
		return nil, err
	}
	dir, err := a.harnessDirectory(h)
	if err != nil {
		return nil, err
	}
	// Turns share the account lease unless the adapter declares it exclusive.
	kind := syscall.LOCK_SH
	if adapter, err := Adapter(h); err == nil && adapter.Exclusive {
		kind = syscall.LOCK_EX
	}
	var gate, turns *os.File
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		gate, err = acquireAccountLock(ctx, filepath.Join(dir, ".admission.lock"), syscall.LOCK_EX)
		if err != nil {
			return nil, err
		}
		probe, e := openAccountLock(filepath.Join(dir, ".switch.lock"), true)
		if e != nil {
			releaseAccountLock(gate)
			return nil, e
		}
		e = syscall.Flock(int(probe.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
		if e == nil {
			turns, e = openAccountLock(filepath.Join(dir, ".turn.lock"), true)
			if e == nil {
				e = syscall.Flock(int(turns.Fd()), kind|syscall.LOCK_NB)
			}
		}
		releaseAccountLock(probe)
		if e == nil && kind == syscall.LOCK_EX {
			count, scanErr := resourceLeaseCount(filepath.Join(dir, ".turns"))
			if scanErr != nil {
				e = scanErr
			} else if count > 0 {
				e = syscall.EWOULDBLOCK
			}
		}
		if e == nil {
			break
		}
		releaseAccountLock(turns)
		turns = nil
		releaseAccountLock(gate)
		if !errors.Is(e, syscall.EWOULDBLOCK) && !errors.Is(e, syscall.EAGAIN) {
			return nil, e
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
	defer releaseAccountLock(gate)
	lease, err := newResourceLease(ctx, filepath.Join(dir, ".turns"), "turn-", nil)
	if err != nil {
		releaseAccountLock(turns)
		return nil, err
	}
	lease.afterClose = func() { releaseAccountLock(turns) }
	return lease, nil
}

// SwitchStatus counts only live file-lock holders. A crashed process cannot pin it.
func (a *Accounts) SwitchStatus(harness string) (pending bool, running int) {
	h, err := canonicalAccountHarness(harness)
	if err != nil {
		return false, 0
	}
	dir := filepath.Join(a.Root, h)
	if safeAccountPath(dir) != nil {
		return false, 0
	}
	busy := func(path string) bool {
		f, e := openAccountLock(path, false)
		if e != nil {
			return false
		}
		defer f.Close()
		e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if e == nil {
			syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			return false
		}
		return errors.Is(e, syscall.EWOULDBLOCK) || errors.Is(e, syscall.EAGAIN)
	}
	pending = busy(filepath.Join(dir, ".switch.lock"))
	running, err = resourceLeaseCount(filepath.Join(dir, ".turns"))
	if err != nil && running == 0 {
		running = 1
	}
	return pending, running
}

// switchGate blocks admission before draining every native process for this harness.
func (a *Accounts) switchGate(ctx context.Context, harness string) (func(), error) {
	if a.SwitchGate != nil {
		return a.SwitchGate(ctx, harness)
	}
	return a.realSwitchGate(ctx, harness)
}

// realSwitchGate drains the harness's turns, so no turn runs while another
// selects an account or renews a login it holds.
func (a *Accounts) realSwitchGate(ctx context.Context, harness string) (func(), error) {
	dir, err := a.harnessDirectory(harness)
	if err != nil {
		return nil, err
	}
	pending, err := acquireAccountLock(ctx, filepath.Join(dir, ".switch.lock"), syscall.LOCK_EX)
	if err != nil {
		return nil, err
	}
	gate, err := acquireAccountLock(ctx, filepath.Join(dir, ".admission.lock"), syscall.LOCK_EX)
	if err != nil {
		releaseAccountLock(pending)
		return nil, err
	}
	turns, err := acquireAccountLock(ctx, filepath.Join(dir, ".turn.lock"), syscall.LOCK_EX)
	if err != nil {
		releaseAccountLock(gate)
		releaseAccountLock(pending)
		return nil, err
	}
	if err = drainResourceLeases(ctx, filepath.Join(dir, ".turns")); err != nil {
		releaseAccountLock(turns)
		releaseAccountLock(gate)
		releaseAccountLock(pending)
		return nil, err
	}

	return func() { releaseAccountLock(turns); releaseAccountLock(gate); releaseAccountLock(pending) }, nil
}

// saveOutgoing keeps the native refresh owner in its saved slot. It never writes
// a grant outside the registry.
func (a *Accounts) saveOutgoing(state *store.State, h string, c native.Captured, ownerIDs ...string) error {
	if c.Identity == "" {
		return native.ErrNoIdentity
	}
	var entry *store.Entry
	owner, err := a.activeID(h)
	if err != nil {
		return err
	}
	if len(ownerIDs) > 0 {
		owner = ownerIDs[0]
	}
	if e := state.Find(owner); e != nil && e.NativeHome != "" && e.Harness == h && e.Identity == c.Identity && e.Integration == c.Integration {
		entry = e
	}
	if entry == nil && !genericNativeLogin(c) {
		entry = nativeEntryFor(state, h, c.Integration, c.Identity)
	}
	if entry == nil {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return err
		}
		key := hex.EncodeToString(id[:])
		home := filepath.Join(a.Root, h, key)
		if err := safeAccountPath(home); err != nil {
			return err
		}
		if err := os.Mkdir(home, 0700); err != nil {
			return err
		}
		now := time.Now().UTC()
		provider, kind := store.ProviderFor(h, c.Integration)
		state.Entries = append(state.Entries, store.Entry{Metadata: store.Metadata{ID: key, Provider: provider, Kind: kind, Harness: h, Integration: c.Integration, Identity: c.Identity, Label: c.Label, NativeHome: home, CreatedAt: now, UpdatedAt: now}})
		entry = &state.Entries[len(state.Entries)-1]
	}
	n, err := a.native(entry)
	if err != nil {
		return err
	}
	if err = writeAccountLogin(n.Home, c); err != nil {
		return err
	}
	if len(ownerIDs) > 0 && ownerIDs[0] == entry.ID {
		pendingPath := filepath.Join(n.Home, ".login-pending.json")
		if _, e := os.Lstat(pendingPath); e == nil {
			if err := writePrivateAccountJSON(pendingPath, c); err != nil {
				return err
			}
		}
	}
	entry.NeedsLogin = false
	entry.UpdatedAt = time.Now().UTC()
	return nil
}

// dropNewEntries removes the accounts and private homes added since existing was read.
func (a *Accounts) dropNewEntries(state *store.State, existing map[string]bool) error {
	kept := state.Entries[:0]
	var err error
	for _, e := range state.Entries {
		if existing[e.ID] {
			kept = append(kept, e)
			continue
		}
		removeErr := errors.New("A new account home is outside Dyna's account folder.")
		if e.NativeHome != "" && safeAccountPath(e.NativeHome) == nil && filepath.Dir(filepath.Dir(e.NativeHome)) == filepath.Clean(a.Root) {
			removeErr = os.RemoveAll(e.NativeHome)
		}
		if removeErr != nil {
			err = errors.Join(err, removeErr)
			kept = append(kept, e)
		}
	}
	state.Entries = kept
	return err
}

func capturedFor(rows []native.Captured, integration string) *native.Captured {
	for i := range rows {
		if rows[i].Integration == integration {
			return &rows[i]
		}
	}
	return nil
}

// idleSwitchWait bounds how long SwitchIfIdle lets a just-started turn delay it.
const idleSwitchWait = 2 * time.Second

func turnsRunningError(h string, running int) error {
	return fmt.Errorf("switch not started: %d %s turns running; run it again when they finish, or use the desktop, which can stop them", running, h)
}

// SwitchIfIdle never waits for running turns. A harness that pins credentials
// per account switches at once. Another harness switches only when no turn
// runs, because its switch closes new turns until the running ones finish.
func (a *Accounts) SwitchIfIdle(ctx context.Context, id, executable string) error {
	n, err := a.Find(id)
	if err != nil {
		return err
	}
	if pinsPerAccount(n.Harness) {
		return a.Switch(ctx, id, executable)
	}
	if _, running := a.SwitchStatus(n.Harness); running > 0 {
		return turnsRunningError(n.Harness, running)
	}
	waitCtx, cancel := context.WithTimeout(ctx, idleSwitchWait)
	defer cancel()
	activating := false
	err = a.SwitchWithProgress(waitCtx, id, executable, func() { activating = true })
	// Only the wait for turns maps to this message. Activation has its own
	// deadline, and its errors stay as they are.
	if !activating && errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		_, running := a.SwitchStatus(n.Harness)
		return turnsRunningError(n.Harness, max(running, 1))
	}
	return err
}

// Switch swaps only login state. Native history and other runtime files stay in place.
// It never renews a saved credential or permits two runtime account homes.
func (a *Accounts) Switch(ctx context.Context, id, executable string) error {
	return a.SwitchWithProgress(ctx, id, executable, nil)
}

// SwitchWithProgress cancels only before the activation boundary. Once the
// callback runs, activation and rollback finish under their own bounded context.
func (a *Accounts) SwitchWithProgress(ctx context.Context, id, executable string, onActivate func()) error {
	return a.switchTo(ctx, id, executable, onActivate)
}

// switchTo runs SwitchWithProgress.
func (a *Accounts) switchTo(ctx context.Context, id, executable string, onActivate func()) error {
	target, err := a.Find(id)
	if err != nil {
		return err
	}
	if pinsPerAccount(target.Harness) {
		return a.switchPinned(ctx, id, executable, onActivate)
	}
	release, err := a.switchGate(ctx, target.Harness)
	if err != nil {
		return err
	}
	defer release()
	if err = ctx.Err(); err != nil {
		return err
	}
	if onActivate != nil {
		onActivate()
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	ctx, cancelActivation := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancelActivation()
	home, err := a.HarnessHome(target.Harness)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(home, 0700); err != nil {
		return err
	}
	if err = checkAccountFiles(target.Harness, home, true); err != nil {
		return err
	}
	shared, _, err := a.scopedManager(target.Harness, home, executable)
	if err != nil {
		return err
	}
	if err = safeAccountPath(filepath.Join(a.Root, "accounts.lock")); err != nil {
		return err
	}
	return (&store.Store{Root: a.Root}).Transaction(func(state *store.State, checkpoint func() error) (operationErr error) {
		// A failed switch writes no accounts. The outgoing login saved below stays
		// only when the native login could not be restored, as its last copy.
		existing := map[string]bool{}
		for _, e := range state.Entries {
			existing[e.ID] = true
		}
		keepSaved := false
		defer func() {
			if operationErr == nil || keepSaved {
				return
			}
			operationErr = errors.Join(operationErr, a.dropNewEntries(state, existing), checkpoint())
		}()
		entry := state.Find(id)
		n, err := a.native(entry)
		if err != nil {
			return err
		}
		oldID, err := a.activeID(n.Harness)
		if err != nil {
			return err
		}
		current, err := shared.Capture(ctx, "")
		if err != nil && !errors.Is(err, native.ErrNotSignedIn) {
			return err
		}
		for _, c := range current {
			if err = a.saveOutgoing(state, n.Harness, c); err != nil {
				return err
			}
		}
		if err = checkpoint(); err != nil {
			return err
		}
		_, pendingErr := os.Lstat(filepath.Join(n.Home, ".login-pending.json"))
		if oldID == id && os.IsNotExist(pendingErr) {
			selected := capturedFor(current, entry.Integration)
			if selected == nil || selected.Identity != n.Identity {
				return native.ErrNoIdentity
			}
			return nil
		}
		desired, err := readAccountLogin(n.Home)
		if err != nil {
			return err
		}
		if desired.Identity != n.Identity || desired.Integration != entry.Integration || entry.NeedsLogin {
			return native.ErrNoIdentity
		}
		previous := capturedFor(current, desired.Integration)
		latest, activationErr := shared.Activate(ctx, desired.Integration, desired.Credential)
		if p := capturedFor(latest, desired.Integration); p != nil {
			previous = p
		}
		// Recover the previous native owner even when a later metadata checkpoint fails.
		defer func() {
			if operationErr == nil || activationErr != nil && !errors.Is(activationErr, native.ErrPartialActivation) {
				return
			}
			if previous == nil {
				keepSaved = true
				operationErr = errors.Join(operationErr, a.setActive(n.Harness, ""))
				operationErr = errors.Join(operationErr, errors.New("No previous sign-in was available to restore."))
				return
			}
			rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
			defer cancel()
			changed, restoreErr := shared.Activate(rollbackCtx, previous.Integration, previous.Credential)
			for _, c := range changed {
				operationErr = errors.Join(operationErr, a.saveOutgoing(state, n.Harness, c, id))
			}
			actual, captureErr := shared.Capture(rollbackCtx, "")
			restored := capturedFor(actual, previous.Integration)
			if restoreErr != nil || captureErr != nil || restored == nil || !sameNativeLogin(*restored, *previous) {
				keepSaved = true
				operationErr = errors.Join(operationErr, errors.New("The previous sign-in could not be restored."), a.setActive(n.Harness, ""))
			} else {
				operationErr = errors.Join(operationErr, a.setActive(n.Harness, oldID))
			}
			operationErr = errors.Join(operationErr, checkpoint())
		}()
		for _, c := range latest {
			if err = a.saveOutgoing(state, n.Harness, c); err != nil {
				return err
			}
		}
		if err = checkpoint(); err != nil {
			return err
		}
		if activationErr != nil {
			return activationErr
		}
		actual, err := shared.Capture(ctx, "")
		if err != nil {
			return err
		}
		verified := capturedFor(actual, desired.Integration)
		if verified == nil || !sameNativeLogin(*verified, desired) {
			return native.ErrNoIdentity
		}
		if err = checkAccountFiles(target.Harness, home, true); err != nil {
			return err
		}
		if err = writeAccountLogin(n.Home, *verified); err != nil {
			return err
		}
		if err = a.setActive(n.Harness, id); err != nil {
			return err
		}
		if err = os.Remove(filepath.Join(n.Home, ".login-pending.json")); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	})
}

func (a *Accounts) Remove(id string, confirmed bool) error {
	if !confirmed {
		return errors.New("Confirm private account removal first.")
	}
	n, err := a.Find(id)
	if err != nil {
		return err
	}
	// A removal must not wait for a running harness or change sign-in underneath it.
	pending, running := a.SwitchStatus(n.Harness)
	if pending || running > 0 {
		return errors.New("Wait for all harness turns and account switches before removal.")
	}
	// Use nonblocking locks so the no-turn condition cannot race new admission.
	dir, err := a.harnessDirectory(n.Harness)
	if err != nil {
		return err
	}
	locks := []*os.File{}
	defer func() {
		for i := len(locks) - 1; i >= 0; i-- {
			releaseAccountLock(locks[i])
		}
	}()
	for _, name := range []string{".switch.lock", ".admission.lock", ".turn.lock"} {
		f, e := openAccountLock(filepath.Join(dir, name), true)
		if e != nil {
			return e
		}
		if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
			f.Close()
			return errors.New("Wait for all harness turns before removal.")
		}
		locks = append(locks, f)
	}
	if err = a.transaction(func(state *store.State) error {
		current, e := a.native(state.Find(id))
		if e != nil {
			return e
		}
		if current.Active {
			if e = a.clearActive(current); e != nil {
				return e
			}
		}
		paired := current.Home + ".home"
		if e = safeAccountPath(paired); e != nil {
			return e
		}
		if e = os.RemoveAll(paired); e != nil {
			return e
		}
		if e = os.RemoveAll(current.Home); e != nil {
			return e
		}
		state.Remove(id)
		return nil
	}, true); err != nil {
		return err
	}
	// A removed account must not keep a repo protected.
	return a.SetRepos(id, nil)
}

// NativeLogin owns a shared-service admission gate until Complete or Cancel.
// The caller stops the native terminal before Cancel. Warning is shown before launch.
type NativeLogin struct {
	Argv     []string
	Env      []string
	Cwd      string
	Warning  string
	Complete func(context.Context) (NativeAccount, error)
	Cancel   func() error
}

// BeginLogin keeps shared native login state under the harness switch gate
// for the full interactive sign-in.
func (a *Accounts) BeginLogin(ctx context.Context, id, executable string) (*NativeLogin, error) {
	n, err := a.Find(id)
	if err != nil {
		return nil, err
	}
	if !sharedServiceAccount(n.Harness) {
		argv, env, cwd, err := a.Login(id, executable)
		if err != nil {
			return nil, err
		}
		var mu sync.Mutex
		finished := false
		var result NativeAccount
		var resultErr error
		return &NativeLogin{Argv: argv, Env: env, Cwd: cwd,
			Complete: func(ctx context.Context) (NativeAccount, error) {
				mu.Lock()
				defer mu.Unlock()
				if finished {
					return result, resultErr
				}
				result, resultErr = a.Capture(ctx, id, executable)
				finished = true
				return result, resultErr
			},
			Cancel: func() error {
				mu.Lock()
				defer mu.Unlock()
				if !finished {
					resultErr = errors.New("Native sign-in was cancelled.")
					finished = true
				}
				return nil
			},
		}, nil
	}
	release, err := a.switchGate(ctx, n.Harness)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			release()
		}
	}()
	home, err := a.HarnessHome(n.Harness)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(home, 0700); err != nil {
		return nil, err
	}
	if err = checkAccountFiles(n.Harness, home, true); err != nil {
		return nil, err
	}
	manager, overrides, err := a.scopedManager(n.Harness, home, executable)
	if err != nil {
		return nil, err
	}
	if err = prepareAccountLogin(n.Harness, home, overrides); err != nil {
		return nil, err
	}
	outgoing, err := manager.Capture(ctx, "")
	if err != nil && !errors.Is(err, native.ErrNotSignedIn) {
		return nil, err
	}
	oldID, err := a.activeID(n.Harness)
	if err != nil {
		return nil, err
	}
	if err = a.transaction(func(state *store.State) error {
		for _, c := range outgoing {
			if e := a.saveOutgoing(state, n.Harness, c); e != nil {
				return e
			}
		}
		return nil
	}, true); err != nil {
		return nil, err
	}
	argv, _, err := manager.LoginCommand("", "")
	if err != nil {
		return nil, err
	}
	if adapter, e := Adapter(n.Harness); e == nil && adapter.LoginArgs != nil {
		argv = adapter.LoginArgs(executable, home)
	}
	env := []string{}
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	flow := &NativeLogin{Argv: argv, Env: env, Cwd: home}

	var mu sync.Mutex
	finished := false
	var result NativeAccount
	var resultErr error
	restore := func() error {
		restoreCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return a.transaction(func(state *store.State) error {
			current, e := manager.Capture(restoreCtx, "")
			if e != nil && !errors.Is(e, native.ErrNotSignedIn) {
				return e
			}
			for _, c := range current {
				if e = a.saveOutgoing(state, n.Harness, c, ""); e != nil {
					return e
				}
			}
			if len(outgoing) == 0 && len(current) > 0 {
				return errors.New("There was no previous native sign-in to restore. The new sign-in remains in the native service.")
			}
			for _, previous := range outgoing {
				if latestCurrent := capturedFor(current, previous.Integration); latestCurrent != nil && latestCurrent.Identity == previous.Identity && !genericNativeLogin(previous) {
					previous = *latestCurrent
				}
				latest, activationErr := manager.Activate(restoreCtx, previous.Integration, previous.Credential)
				for _, c := range latest {
					if e = a.saveOutgoing(state, n.Harness, c, ""); e != nil {
						return e
					}
				}
				if activationErr != nil {
					return errors.Join(errors.New("The previous native sign-in could not be restored."), a.setActive(n.Harness, ""))
				}
			}
			actual, e := manager.Capture(restoreCtx, "")
			if len(outgoing) > 0 {
				if e != nil {
					return errors.Join(errors.New("The previous native sign-in could not be verified."), a.setActive(n.Harness, ""))
				}
				for _, previous := range outgoing {
					c := capturedFor(actual, previous.Integration)
					if c == nil || c.Identity != previous.Identity {
						return errors.Join(errors.New("The previous native sign-in could not be verified."), a.setActive(n.Harness, ""))
					}
				}
			}
			return a.setActive(n.Harness, oldID)
		}, true)
	}
	flow.Complete = func(ctx context.Context) (NativeAccount, error) {
		mu.Lock()
		defer mu.Unlock()
		if finished {
			return result, resultErr
		}
		defer func() { finished = true; release() }()
		resultErr = a.transaction(func(state *store.State) error {
			entry := state.Find(id)
			if entry == nil {
				return errAccountMissing
			}
			captured, e := manager.Capture(ctx, "")
			if e != nil {
				return e
			}
			selected := selectLoginCapture(captured, outgoing, entry.Integration)
			if selected == nil || selected.Identity == "" {
				return native.ErrNoIdentity
			}
			if e = checkPrivateAccountHome(n.Harness, home, true); e != nil {
				return e
			}
			if e = checkAccountFiles(n.Harness, home, true); e != nil {
				return e
			}
			if e = writeAccountLogin(n.Home, *selected); e != nil {
				return e
			}
			entry.Identity = selected.Identity
			entry.Integration = selected.Integration
			if entry.Label == "" {
				entry.Label = selected.Label
			}
			entry.ClearSignInFailure()
			entry.UpdatedAt = time.Now().UTC()
			return nil
		}, true)
		if resultErr == nil {
			resultErr = a.recordSignIn(ctx, n)
		}
		if resultErr == nil {
			resultErr = a.setActive(n.Harness, id)
		}
		if resultErr == nil {
			result, resultErr = a.Find(id)
		}
		if resultErr != nil {
			resultErr = errors.Join(resultErr, restore())
		}
		return result, resultErr
	}
	flow.Cancel = func() error {
		mu.Lock()
		defer mu.Unlock()
		if finished {
			return nil
		}
		defer func() { finished = true; release() }()
		resultErr = errors.New("Native sign-in was cancelled.")
		return restore()
	}
	keep = true
	return flow, nil
}

func selectLoginCapture(current, previous []native.Captured, integration string) *native.Captured {
	if integration != "" {
		return capturedFor(current, integration)
	}
	if len(current) == 1 {
		return &current[0]
	}
	var selected *native.Captured
	for i := range current {
		old := capturedFor(previous, current[i].Integration)
		if old == nil || old.Identity != current[i].Identity {
			if selected != nil {
				return nil
			}
			selected = &current[i]
		}
	}
	return selected
}

// Sync login snapshots before activating another refresh owner.
func writePrivateAccountJSON(path string, value any) error {
	return writePrivateAccountJSONChecked(path, value, nil)
}

// writePrivateAccountJSONChecked runs check after the temporary file is synced
// and just before it replaces path. A check error keeps the old file.
func writePrivateAccountJSONChecked(path string, value any, check func() error) error {
	if err := safeAccountPath(path); err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return errors.New("Private account data could not be encoded.")
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".account-json-*")
	if err != nil {
		return errors.New("Private account data could not be written.")
	}
	keepRecovery := false
	defer func() {
		f.Close()
		if !keepRecovery {
			os.Remove(f.Name())
		}
	}()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = f.Close()
	}
	if err != nil {
		return errors.New("Private account data could not be written.")
	}
	if check != nil {
		if err = check(); err != nil {
			return err
		}
	}
	if err = os.Rename(f.Name(), path); err != nil {
		keepRecovery = true
		return errors.New("Private account data could not be replaced. A private recovery file was retained.")
	}
	parent, err := os.Open(dir)
	if err == nil {
		err = parent.Sync()
		parent.Close()
	}
	if err != nil {
		return errors.New("Private account data could not be synced.")
	}
	return nil
}

// genericNativeLogin reports a native-files login with the placeholder
// identity, which cannot tell two users apart.
func genericNativeLogin(c native.Captured) bool {
	return strings.HasSuffix(c.Credential.Schema, "/native-files@1") && strings.HasSuffix(c.Identity, ":native")
}
func sameNativeLogin(actual, desired native.Captured) bool {
	if actual.Identity != desired.Identity || actual.Integration != desired.Integration {
		return false
	}
	if !genericNativeLogin(desired) {
		return true
	}
	var actualJSON, desiredJSON bytes.Buffer
	if json.Compact(&actualJSON, actual.Credential.Payload) != nil || json.Compact(&desiredJSON, desired.Credential.Payload) != nil {
		return false
	}
	return bytes.Equal(actualJSON.Bytes(), desiredJSON.Bytes())
}
