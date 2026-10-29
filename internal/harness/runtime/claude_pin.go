package runtime

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
	"dyna-agent/internal/fsutil"
)

// Claude Code reads .credentials.json from CLAUDE_SECURESTORAGE_CONFIG_DIR. It
// watches the file and re-reads it when it changes, and it writes refreshed
// tokens back to it. So each Claude account keeps its own credentials in its
// account home, next to a .claude.json that holds only its oauthAccount. A turn
// uses the folder of the account that was active when it started. A switch never
// changes a file that a running turn reads, so it does not wait for turns.
// History and resume stay in the shared CLAUDE_CONFIG_DIR.
const pinnedSecretKey = "CLAUDE_SECURESTORAGE_CONFIG_DIR"

func pinsCredentials(h string) bool { return h == "claude-code" }

// pinnedSecret is the active account's home when scope is the shared harness home.
func (a *Accounts) pinnedSecret(h, scope string) string {
	if !pinsCredentials(h) {
		return ""
	}
	shared, err := a.HarnessHome(h)
	if err != nil || filepath.Clean(scope) != shared {
		return ""
	}
	id, err := a.activeID(h)
	if err != nil || id == "" {
		return ""
	}
	return filepath.Join(a.Root, h, id)
}

// pinnedSecretOf reports whether secret is an account home next to the shared scope.
func pinnedSecretOf(scope, secret string) bool {
	name := filepath.Base(secret)
	_, err := hex.DecodeString(name)
	return secret != "" && filepath.Dir(filepath.Clean(secret)) == filepath.Dir(filepath.Clean(scope)) && len(name) == 32 && err == nil
}

// pinManager reads and writes only the account's own credentials and oauthAccount.
func (a *Accounts) pinManager(n NativeAccount, executable string) (native.Manager, error) {
	home, err := a.HarnessHome(n.Harness)
	if err != nil {
		return nil, err
	}
	m, _, err := a.scopedManagerWith(n.Harness, home, executable, map[string]string{"CLAUDE_CONFIG_DIR": n.Home, pinnedSecretKey: n.Home})
	return m, err
}

type newerActivator interface {
	ActivateNewer(context.Context, native.Captured) (bool, error)
}

// pinnedLogin reports whether the account's own credentials folder holds a login.
func pinnedLogin(n NativeAccount) bool {
	_, err := os.Lstat(filepath.Join(n.Home, ".credentials.json"))
	return pinsCredentials(n.Harness) && err == nil
}

// syncPin makes the account's credentials folder hold its newest login, and
// returns it. Each write happens only when the login expires later than the
// one in place, decided under Claude Code's own locks. The caller holds the
// account store lock.
func (a *Accounts) syncPin(ctx context.Context, n NativeAccount, executable string) (native.Captured, error) {
	var none native.Captured
	if err := checkAccountFiles(n.Harness, n.Home, true); err != nil {
		return none, err
	}
	m, err := a.pinManager(n, executable)
	if err != nil {
		return none, err
	}
	newer, ok := m.(newerActivator)
	if !ok {
		return none, native.ErrUnsupported
	}
	saved, err := readAccountLogin(n.Home)
	if err != nil {
		return none, err
	}
	if _, err = newer.ActivateNewer(ctx, saved); err != nil {
		return none, err
	}
	if err = a.adoptSharedCredentials(ctx, n, executable, newer); err != nil {
		return none, err
	}
	rows, err := m.Capture(ctx, "")
	if err != nil {
		return none, err
	}
	if len(rows) != 1 || rows[0].Identity != n.Identity {
		return none, native.ErrNoIdentity
	}
	return rows[0], nil
}

// prepareTurnPin readies the active account's credentials folder before a turn.
func (a *Accounts) prepareTurnPin(ctx context.Context, active NativeAccount, executable string) error {
	return a.transaction(func(*store.State) error {
		_, err := a.syncPin(ctx, active, executable)
		return err
	}, true)
}

// TODO: legacy/shim remove later — Dyna builds before per-account Claude
// credentials kept the active account's login in the shared runtime home, and
// turns that they started may still refresh it there. The file belongs to the
// account that was active when this build first ran. Adopt it while it
// expires later. Remove once no turn started by such a build can run.
func (a *Accounts) adoptSharedCredentials(ctx context.Context, n NativeAccount, executable string, newer newerActivator) error {
	shared, err := a.HarnessHome(n.Harness)
	if err != nil {
		return err
	}
	marker := filepath.Join(shared, ".credentials-owner.json")
	var owner struct {
		ID string `json:"id"`
	}
	raw, err := os.ReadFile(marker)
	switch {
	case err == nil:
		if json.Unmarshal(raw, &owner) != nil {
			return errors.New("The shared Claude credentials owner record is damaged.")
		}
	case os.IsNotExist(err):
		if _, e := os.Lstat(filepath.Join(shared, ".credentials.json")); e == nil {
			if owner.ID, err = a.activeID(n.Harness); err != nil {
				return err
			}
		}
		raw, _ = json.Marshal(owner)
		if err = fsutil.WriteFile(marker, raw, 0600, false); err != nil {
			return err
		}
	default:
		return err
	}
	if owner.ID != n.ID {
		return nil
	}
	// Pair the shared credentials with the owner's own oauthAccount.
	source, _, err := a.scopedManagerWith(n.Harness, shared, executable, map[string]string{"CLAUDE_CONFIG_DIR": n.Home})
	if err != nil {
		return err
	}
	rows, err := source.Capture(ctx, "")
	if errors.Is(err, native.ErrNotSignedIn) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(rows) != 1 || rows[0].Identity != n.Identity {
		return nil
	}
	_, err = newer.ActivateNewer(ctx, rows[0])
	return err
}

// switchPinned records the new active account without waiting for turns.
// Running turns keep the credentials folder of the account they started with.
func (a *Accounts) switchPinned(ctx context.Context, id, executable string, onActivate func(), keep func(active string) bool) error {
	if onActivate != nil {
		onActivate()
	}
	n, err := a.Find(id)
	if err != nil {
		return err
	}
	if n.Harness == "pi" {
		if err := a.keepActive(n.Harness, keep); err != nil {
			return err
		}
		return a.selectPinned(n)
	}
	return a.transaction(func(state *store.State) error {
		entry := state.Find(id)
		n, err := a.native(entry)
		if err != nil {
			return err
		}
		if entry.NeedsLogin {
			return native.ErrNoIdentity
		}
		home, err := a.HarnessHome(n.Harness)
		if err != nil {
			return err
		}
		if err = os.MkdirAll(home, 0700); err != nil {
			return err
		}
		if err := a.keepActive(n.Harness, keep); err != nil {
			return err
		}
		login, err := a.syncPin(ctx, n, executable)
		if err != nil {
			return err
		}
		// Write this account's oauthAccount to the shared config. Claude Code
		// uses it only for organization caches; the token picks the account.
		shared, _, err := a.scopedManagerWith(n.Harness, home, executable, map[string]string{pinnedSecretKey: n.Home})
		if err != nil {
			return err
		}
		writer, ok := shared.(interface {
			WriteOAuthAccount(context.Context, store.Credential) error
		})
		if !ok {
			return native.ErrUnsupported
		}
		if err = writer.WriteOAuthAccount(ctx, login.Credential); err != nil {
			return err
		}
		if err = writeAccountLogin(n.Home, login); err != nil {
			return err
		}
		if err = os.Remove(filepath.Join(n.Home, ".login-pending.json")); err != nil && !os.IsNotExist(err) {
			return err
		}
		return a.setActiveAccount(n)
	}, true)
}
