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

// piEditAuth reads and edits one Pi home's login file through Pi's own auth
// commands, so the whole edit takes Pi's lock once and races no login or token
// refresh. It lists the current entries, applies edit in memory, and applies
// the whole result with one auth apply command. A nil edit only reads.
func (a *Accounts) piEditAuth(ctx context.Context, home string, edit piAuthEdit) (map[string]json.RawMessage, error) {
	executable, err := a.piExecutable()
	if err != nil {
		return nil, err
	}
	out, err := piAuthRunner(ctx, executable, home, []string{"auth", "list", "--json"}, nil)
	if err != nil {
		return nil, err
	}
	entries := map[string]json.RawMessage{}
	if json.Unmarshal(out, &entries) != nil || entries == nil {
		return nil, errors.New("Pi's login file is not valid JSON.")
	}
	if edit == nil {
		return entries, nil
	}
	next := maps.Clone(entries)
	edit(next)
	return a.piApplyAuth(ctx, executable, home, entries, next, nil)
}

// piApplyAuth performs one whole edit with Pi's own auth apply command, which
// reads the file, checks expect when it is set, and writes the result under
// one lock, so no concurrent writer can run between the read and the write and
// no failure can leave half of the edit. The command sets each entry that
// changed against current and removes each dropped one, so it keeps entries
// another process added after the list; expect, when not nil, must still be
// the file's exact content or the command refuses and writes nothing. It
// returns the entries as the file holds them now.
func (a *Accounts) piApplyAuth(ctx context.Context, executable, home string, current, next, expect map[string]json.RawMessage) (map[string]json.RawMessage, error) {
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
		return next, nil
	}
	body, err := json.Marshal(op)
	if err != nil {
		return nil, err
	}
	out, err := piAuthRunner(ctx, executable, home, []string{"auth", "apply"}, body)
	if err != nil {
		return nil, err
	}
	result := map[string]json.RawMessage{}
	if json.Unmarshal(out, &result) != nil || result == nil {
		return nil, errors.New("Pi's login file is not valid JSON.")
	}
	return result, nil
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
	file := filepath.Join(account.Home, "auth.json")
	before, readErr := os.ReadFile(file)
	written, err := a.Accounts.piEditAuth(ctx, account.Home, edit)
	if err != nil {
		return err
	}
	// restore puts the account's login back and reports a failure to do so.
	// It runs on a fresh context, so a cause that canceled ctx still restores,
	// and it refuses unless the login still holds what the edit wrote, so it
	// never replaces a login another process wrote in the meantime.
	restore := func(cause error) error {
		err := a.Accounts.restorePiAuth(context.Background(), account.Home, before, readErr == nil, written)
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

// restorePiAuth puts the earlier entries back through Pi's own auth commands,
// or empties the login when it did not exist, so a failed record leaves the
// account's sign-in as it was. expect is the state the failed edit wrote: the
// restore refuses unless the login still holds exactly it, so it never
// overwrites an entry another writer, such as a token refresh, replaced in
// the meantime.
func (a *Accounts) restorePiAuth(ctx context.Context, home string, before []byte, existed bool, expect map[string]json.RawMessage) error {
	entries := map[string]json.RawMessage{}
	if existed && (len(before) == 0 || json.Unmarshal(before, &entries) != nil || entries == nil) {
		return errors.New("Pi's saved login could not be restored. Add or remove the key again.")
	}
	executable, err := a.piExecutable()
	if err != nil {
		return err
	}
	current := expect
	if current == nil {
		current = map[string]json.RawMessage{}
	}
	_, err = a.piApplyAuth(ctx, executable, home, current, entries, expect)
	return err
}
