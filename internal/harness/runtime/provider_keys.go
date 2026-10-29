package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"syscall"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/mode"
)

// errProviderKeys is the refusal of a harness that keeps no API keys.
var errProviderKeys = errors.New("This harness does not take provider API keys.")

const maxProviderKey = 8 << 10

// piKeysMu serializes Dyna's own Pi key edits, so two edits of one account
// never apply two command sequences in different orders.
var piKeysMu sync.Mutex

// piAuthEdit changes the provider entries of one auth.json. A nil edit only reads.
type piAuthEdit func(entries map[string]json.RawMessage)

// piAPIKeyEdit is the edit that writes one provider's API key into an
// account's auth.json.
func piAPIKeyEdit(provider, key string) piAuthEdit {
	entry, _ := json.Marshal(map[string]string{"type": "api_key", "key": key})
	return func(entries map[string]json.RawMessage) { entries[provider] = entry }
}

// piAuthRunner runs one auth command of Dyna's own Pi copy for one account
// home. Tests replace it.
var piAuthRunner = runPiAuthCommand

// runPiAuthCommand runs executable's auth subcommand with Pi's private
// environment, argv and stdin, and returns its stdout. It never passes secrets
// through argv.
func runPiAuthCommand(ctx context.Context, executable, home string, argv []string, stdin []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, executable, argv...)
	cmd.Env = append(envWithout(os.Environ(), mode.EnvSession, mode.EnvMode, mode.EnvRoom, mode.EnvDepth),
		"PI_CODING_AGENT_DIR="+home,
	)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, errors.New("Pi's login file could not be updated.")
	}
	return stdout.Bytes(), nil
}

// piAuthState is what one auth apply found and left in the login file: the
// entries it read under Pi's own lock, nil when the file did not exist, and
// the entries the file holds now.
type piAuthState struct {
	Before map[string]json.RawMessage
	After  map[string]json.RawMessage
}

// piEditAuth reads and edits one Pi home's login file. A read is a plain read
// of auth.json, so it works wherever Pi is not installed yet and spawns no
// process. An edit applies the whole result with one auth apply command,
// which reads, checks and writes the file under Pi's own lock, so an edit
// races no login or token refresh. Where Pi is not installed, nothing holds
// its lock, so the edit replaces the file by rename instead and a provider
// key can be saved before Pi is. A nil edit only reads.
func (a *Accounts) piEditAuth(ctx context.Context, home string, edit piAuthEdit) (map[string]json.RawMessage, error) {
	state, err := a.piEditAuthState(ctx, home, edit)
	return state.After, err
}

// piEditAuthState is piEditAuth, and it also reports the entries the edit
// found: the apply command read them under Pi's own lock, so a rollback puts
// back the state the edit replaced, never an older snapshot.
func (a *Accounts) piEditAuthState(ctx context.Context, home string, edit piAuthEdit) (piAuthState, error) {
	entries, err := readPiAuthFile(home)
	if err != nil {
		return piAuthState{}, err
	}
	if edit == nil {
		return piAuthState{Before: entries, After: entries}, nil
	}
	next := maps.Clone(entries)
	edit(next)
	executable, err := a.piExecutable()
	if errors.Is(err, ErrPiMissing) {
		// Pi is not installed, so nothing runs its commands or holds its
		// lock. Dyna still saves the edit, replacing the file by rename.
		if err := writePiAuthFile(home, next); err != nil {
			return piAuthState{}, err
		}
		return piAuthState{Before: entries, After: next}, nil
	}
	if err != nil {
		return piAuthState{}, err
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		return piAuthState{}, errors.New("Pi's private home is not available.")
	}
	return a.piApplyAuth(ctx, executable, home, entries, next, nil)
}

// writePiAuthFile replaces one login file's entries by rename, so a reader
// always sees a whole file. It is the fallback when Pi is not installed:
// nothing then runs Pi's commands or holds its lock, so no lock is needed.
func writePiAuthFile(home string, entries map[string]json.RawMessage) error {
	raw, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		return errors.New("Pi's private home is not available.")
	}
	path := filepath.Join(home, "auth.json")
	temp := path + ".dyna-edit"
	if err := os.WriteFile(temp, raw, 0600); err != nil {
		return errors.New("Pi's login file could not be updated.")
	}
	if err := os.Rename(temp, path); err != nil {
		os.Remove(temp)
		return errors.New("Pi's login file could not be updated.")
	}
	return nil
}

// samePiAuthEntries reports whether two entry sets hold the same JSON values,
// so a restore's refusal compares content, not formatting. The entries are
// decoded because their JSON shape is the stored data's, not a fixed one.
func samePiAuthEntries(a, b map[string]json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for id, entry := range a {
		other, held := b[id]
		if !held {
			return false
		}
		var left, right any
		if json.Unmarshal(entry, &left) != nil || json.Unmarshal(other, &right) != nil || !reflect.DeepEqual(left, right) {
			return false
		}
	}
	return true
}

// readPiAuthFile reads one login file's provider entries. A missing file is
// an empty login. Pi's own login writes replace the file by rename under its
// lock, so a reader sees the old or the new file, never half of one.
func readPiAuthFile(home string) (map[string]json.RawMessage, error) {
	raw, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if os.IsNotExist(err) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, errors.New("Pi's login file could not be read.")
	}
	entries := map[string]json.RawMessage{}
	// A JSON null decodes without error and leaves a nil map.
	if len(raw) > 4<<20 || json.Unmarshal(raw, &entries) != nil || entries == nil {
		return nil, errors.New("Pi's login file is not valid JSON.")
	}
	return entries, nil
}

// piApplyAuth performs one whole edit with Pi's own auth apply command, which
// reads the file, checks expect when it is set, and writes the result under
// one lock, so no concurrent writer can run between the read and the write and
// no failure can leave half of the edit. The command sets each entry that
// changed against current and removes each dropped one, so it keeps entries
// another process added after the list; expect, when not nil, must still be
// the file's exact content or the command refuses and writes nothing. It
// returns the entries the command found under the lock and the entries the
// file holds now.
func (a *Accounts) piApplyAuth(ctx context.Context, executable, home string, current, next, expect map[string]json.RawMessage) (piAuthState, error) {
	op := struct {
		Set    map[string]json.RawMessage  `json:"set,omitempty"`
		Remove []string                    `json:"remove,omitempty"`
		Expect *map[string]json.RawMessage `json:"expect,omitempty"`
	}{}
	if expect != nil {
		op.Expect = &expect
	}
	for _, id := range slices.Sorted(maps.Keys(current)) {
		if entry, wanted := next[id]; !wanted {
			op.Remove = append(op.Remove, id)
		} else if !bytes.Equal(entry, current[id]) {
			if op.Set == nil {
				op.Set = map[string]json.RawMessage{}
			}
			op.Set[id] = entry
		}
	}
	for _, id := range slices.Sorted(maps.Keys(next)) {
		if _, existed := current[id]; !existed {
			if op.Set == nil {
				op.Set = map[string]json.RawMessage{}
			}
			op.Set[id] = next[id]
		}
	}
	if op.Set == nil && op.Remove == nil {
		return piAuthState{Before: current, After: next}, nil
	}
	body, err := json.Marshal(op)
	if err != nil {
		return piAuthState{}, err
	}
	out, err := piAuthRunner(ctx, executable, home, []string{"auth", "apply"}, body)
	if err != nil {
		return piAuthState{}, err
	}
	state := piAuthState{}
	if json.Unmarshal(out, &state) != nil || state.After == nil {
		return piAuthState{}, errors.New("Pi's login file is not valid JSON.")
	}
	return state, nil
}

// lockPiAccounts holds the first lock of an account switch or native login, so
// the active account stays fixed through a login edit without waiting for
// running turns. The caller releases it when the edit and its record are done.
func (a *Runtime) lockPiAccounts(ctx context.Context) (func(), error) {
	dir, err := a.Accounts.harnessDirectory("pi")
	if err != nil {
		return nil, err
	}
	gate, err := acquireAccountLock(ctx, filepath.Join(dir, ".switch.lock"), syscall.LOCK_EX)
	if err != nil {
		return nil, errors.New("Pi's account is changing. Try again in a moment.")
	}
	return func() { releaseAccountLock(gate) }, nil
}

// writePiAccountLogin applies edit to one Pi account's login, records it, and
// makes the account active for its provider. The caller holds the Pi account
// locks, so no switch moves the account during the write.
func (a *Runtime) writePiAccountLogin(ctx context.Context, account NativeAccount, edit piAuthEdit) error {
	state, err := a.Accounts.piEditAuthState(ctx, account.Home, edit)
	if err != nil {
		return err
	}
	// restore puts the login back as the edit found it and reports a failure
	// to do so. The apply read that state under Pi's own lock, so a token
	// refreshed just before the edit is not lost to an older snapshot. It
	// runs on a fresh context, so a cause that canceled ctx still restores,
	// and it refuses unless the login still holds what the edit wrote, so it
	// never replaces a login another process wrote in the meantime.
	restore := func(cause error) error {
		err := a.Accounts.restorePiAuth(context.Background(), account.Home, state.Before, state.After)
		if err == nil {
			_, err = a.Accounts.Capture(context.Background(), account.ID, "")
		}
		if err != nil {
			return errors.Join(cause, errors.New("Pi's saved login could not be restored. Add or remove the key again."))
		}
		return cause
	}
	captured, err := a.Accounts.Capture(ctx, account.ID, "")
	if err != nil {
		if errors.Is(err, native.ErrNotSignedIn) {
			// The last entry is gone, so the account is signed out now.
			return nil
		}
		return restore(errors.New("Pi's login could not be recorded."))
	}
	return a.Accounts.setActiveAccount(captured)
}

// restorePiAuth puts the entries the edit found back through Pi's own auth
// commands, or empties the login when the file did not exist, so a failed
// record leaves the account's sign-in as the edit found it. expect is the
// state the failed edit wrote: the restore refuses unless the login still
// holds exactly it, so it never overwrites an entry another writer, such as a
// token refresh, replaced in the meantime.
func (a *Accounts) restorePiAuth(ctx context.Context, home string, before, expect map[string]json.RawMessage) error {
	executable, err := a.piExecutable()
	if errors.Is(err, ErrPiMissing) {
		return restorePiAuthFile(home, before, expect)
	}
	if err != nil {
		return err
	}
	current := expect
	if current == nil {
		current = map[string]json.RawMessage{}
	}
	_, err = a.piApplyAuth(ctx, executable, home, current, before, expect)
	return err
}

// restorePiAuthFile is restorePiAuth where Pi is not installed: it reads the
// login plainly, refuses unless it still holds what the failed edit wrote,
// and puts the entries the edit found back by rename.
func restorePiAuthFile(home string, before, expect map[string]json.RawMessage) error {
	current, err := readPiAuthFile(home)
	if err != nil {
		return err
	}
	if !samePiAuthEntries(current, expect) {
		return errors.New("Pi's saved login could not be restored. Add or remove the key again.")
	}
	return writePiAuthFile(home, before)
}
