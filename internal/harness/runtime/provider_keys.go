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
// commands, so every write takes Pi's lock and races no login or token refresh.
// It lists the current entries, applies edit in memory, and then sets each
// changed entry and removes each dropped one. A nil edit only reads.
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
	for _, id := range slices.Sorted(maps.Keys(entries)) {
		if entry, wanted := next[id]; !wanted {
			if _, err := piAuthRunner(ctx, executable, home, []string{"auth", "remove", "--provider", id}, nil); err != nil {
				return nil, err
			}
		} else if !bytes.Equal(entry, entries[id]) {
			if _, err := piAuthRunner(ctx, executable, home, []string{"auth", "set", "--provider", id}, entry); err != nil {
				return nil, err
			}
		}
	}
	for _, id := range slices.Sorted(maps.Keys(next)) {
		if _, existed := entries[id]; existed {
			continue
		}
		if _, err := piAuthRunner(ctx, executable, home, []string{"auth", "set", "--provider", id}, next[id]); err != nil {
			return nil, err
		}
	}
	return next, nil
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
	// restore puts the account's login back and reports a failure to do so.
	// It runs on a fresh context, so a cause that canceled ctx still restores.
	restore := func(cause error) error {
		err := a.Accounts.restorePiAuth(context.Background(), account.Home, before, readErr == nil)
		if err == nil {
			_, err = a.Accounts.Capture(context.Background(), account.ID, "")
		}
		if err != nil {
			return errors.Join(cause, errors.New("Pi's saved login could not be restored. Add or remove the key again."))
		}
		return cause
	}
	if _, err := a.Accounts.piEditAuth(ctx, account.Home, edit); err != nil {
		return err
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
// account's sign-in as it was.
func (a *Accounts) restorePiAuth(ctx context.Context, home string, before []byte, existed bool) error {
	entries := map[string]json.RawMessage{}
	if existed && (len(before) == 0 || json.Unmarshal(before, &entries) != nil || entries == nil) {
		return errors.New("Pi's saved login could not be restored. Add or remove the key again.")
	}
	_, err := a.piEditAuth(ctx, home, func(current map[string]json.RawMessage) {
		clear(current)
		maps.Copy(current, entries)
	})
	return err
}
