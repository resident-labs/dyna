package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
	harnessmgr "dyna-agent/internal/harness/config"
	harnesshome "dyna-agent/internal/harness/home"
)

// TODO: legacy/shim remove later — old Muse snapshots predate private
// initialization. A private login or the owner-authorized one-time import records origin.
func requirePrivateAccount(h string) bool { return h == "muse" }

var errPrivateSignIn = errors.New("Sign in again in Dyna's private account scope before using this account.")

func privateProofHolds(identity string, proof *store.PrivateProof) bool {
	return identity != "" && proof != nil && proof.Version == 1 && proof.Identity == identity
}
func requirePrivateSnapshot(n NativeAccount) error {
	if requirePrivateAccount(n.Harness) && !privateProofHolds(n.Identity, n.proof) {
		return errPrivateSignIn
	}
	return nil
}

// Check only credential/config/lock paths. Tool sockets and bridges in the
// paired HOME are valid and must never enter the native credential tree walk.
func privateAccountFiles(h, scope string) []string {
	roots := harnesshome.Roots(scope)
	config := roots["XDG_CONFIG_HOME"]
	switch h {
	case "antigravity":
		home := harnesshome.Home(scope)
		return []string{filepath.Join(home, native.AntigravityACPTokenFile), filepath.Join(home, native.AntigravityAccountFile)}
	case "muse":
		return []string{filepath.Join(config, "muse", "auth.json"), filepath.Join(config, "muse", ".auth.json.lock")}
	case "opencode":
		return []string{filepath.Join(roots["XDG_DATA_HOME"], "opencode", "auth.json"), filepath.Join(roots["XDG_STATE_HOME"], "opencode", ".dyna-account.lock")}
	case "kimi-code":
		files := []string{filepath.Join(scope, "config.toml"), filepath.Join(scope, ".dyna-account.lock"), filepath.Join(scope, "credentials", "kimi-code.json")}
		entries, err := os.ReadDir(filepath.Join(scope, "credentials"))
		if err == nil {
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
					files = append(files, filepath.Join(scope, "credentials", entry.Name()))
				}
			}
		}
		return files
	}
	return nil
}
func checkPrivateAccountFile(path string, setModes bool) error {
	if err := safeAccountPath(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return errAccountPath
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if ok && stat.Nlink == 0 {
		return nil
	} // Replaced by an atomic rename after Lstat.
	if !ok || stat.Nlink != 1 {
		return errAccountPath
	}
	if setModes {
		return chmodAccountNode(path, 0600, true)
	}
	return nil
}
func checkPrivateAccountHome(h, scope string, setModes bool) error {
	for _, root := range harnesshome.Roots(scope) {
		if err := safeAccountPath(root); err != nil {
			return err
		}
	}
	for _, path := range privateAccountFiles(h, scope) {
		if err := checkPrivateAccountFile(path, setModes); err != nil {
			return err
		}
	}
	return nil
}
func prepareAccountLogin(h, scope string, env map[string]string) error {
	if err := checkPrivateAccountHome(h, scope, false); err != nil {
		return err
	}
	if err := harnesshome.Ensure(scope); err != nil {
		return err
	}
	inherited, err := harnessmgr.NewConfigManager().ToolHomeEnvironment(os.Environ())
	if err != nil {
		return err
	}
	prepared, err := harnesshome.Prepare(scope, os.Getenv("DYNA_BIN"), inherited)
	if err != nil {
		return err
	}
	for k, v := range prepared {
		env[k] = v
	}
	// Canonical private roots win over every inherited and provider override.
	for k, v := range harnesshome.Roots(scope) {
		env[k] = v
	}
	return validateAccountManagerEnv(h, scope, env)
}

type checkedHomeManager struct {
	native.Manager
	harness, scope string
}

func (m checkedHomeManager) Capture(ctx context.Context, root string) ([]native.Captured, error) {
	if root != "" {
		return nil, errAccountPath
	}
	if err := checkPrivateAccountHome(m.harness, m.scope, false); err != nil {
		return nil, err
	}
	return m.Manager.Capture(ctx, "")
}
func (m checkedHomeManager) Activate(ctx context.Context, integration string, c store.Credential) ([]native.Captured, error) {
	if err := checkPrivateAccountHome(m.harness, m.scope, false); err != nil {
		return nil, err
	}
	rows, err := m.Manager.Activate(ctx, integration, c)
	if e := checkPrivateAccountHome(m.harness, m.scope, true); e != nil {
		return rows, errors.Join(err, e)
	}
	return rows, err
}
func (m checkedHomeManager) ProbeSaved(ctx context.Context, c native.Captured) (store.Observation, error) {
	if err := checkPrivateAccountHome(m.harness, m.scope, false); err != nil {
		return store.Observation{}, err
	}
	if p, ok := m.Manager.(native.SavedProber); ok {
		return p.ProbeSaved(ctx, c)
	}
	return store.Observation{}, native.ErrUnsupported
}
func (m checkedHomeManager) RecoverCredential(ctx context.Context, c native.Captured) (native.Captured, error) {
	if err := checkPrivateAccountHome(m.harness, m.scope, false); err != nil {
		return native.Captured{}, err
	}
	if p, ok := m.Manager.(native.CredentialRecoverer); ok {
		return p.RecoverCredential(ctx, c)
	}
	return native.Captured{}, native.ErrUnsupported
}
func (m checkedHomeManager) CredentialExpiry(c native.Captured) (time.Time, error) {
	if p, ok := m.Manager.(native.CredentialExpirer); ok {
		return p.CredentialExpiry(c)
	}
	return native.CredentialExpiry(c.Credential), nil
}
func (m checkedHomeManager) Refresh(ctx context.Context, c native.Captured) (native.Captured, error) {
	if err := checkPrivateAccountHome(m.harness, m.scope, false); err != nil {
		return native.Captured{}, err
	}
	return m.Manager.Refresh(ctx, c)
}

// Validate the final manager environment after adapter values have been merged.
// Active native scopes stay at scope; Muse and Cursor use exact checked HOME paths.
func validateAccountManagerEnv(h, scope string, env map[string]string) error {
	if err := safeAccountPath(scope); err != nil {
		return err
	}
	for key, value := range harnesshome.Roots(scope) {
		if env[key] != value {
			return errAccountPath
		}
	}
	expected := map[string]string{}
	switch h {
	case "claude-code":
		expected["CLAUDE_CONFIG_DIR"] = scope
		expected["CLAUDE_SECURESTORAGE_CONFIG_DIR"] = scope
		expected["ANTHROPIC_CONFIG_DIR"] = filepath.Join(scope, "anthropic")
	case "codex":
		expected["CODEX_HOME"] = scope
		expected["CODEX_SQLITE_HOME"] = filepath.Join(scope, "sqlite")
	case "grok":
		expected["GROK_HOME"] = scope
	case "pi":
		expected["PI_CODING_AGENT_DIR"] = scope
		expected["PI_CODING_AGENT_SESSION_DIR"] = filepath.Join(scope, "sessions")
	case "kimi-code":
		expected["KIMI_CODE_HOME"] = scope
	case "opencode":
		expected["OPENCODE_CONFIG_DIR"] = scope
		expected["OPENCODE_DB"] = filepath.Join(scope, "opencode.db")
	case "muse":
		expected["MUSE_AUTH_PATH"] = privateAccountFiles(h, scope)[0]
	case "antigravity":
		for key, want := range native.AntigravityFileEnv() {
			if env[key] != want {
				return errAccountPath
			}
		}
	default:
		return native.ErrUnsupported
	}
	for key, path := range expected {
		if env[key] != path {
			return errAccountPath
		}
		if err := safeAccountPath(path); err != nil {
			return err
		}
	}
	return checkPrivateAccountHome(h, scope, false)
}

func (m checkedHomeManager) SupportsCredentialRefresh(c native.Captured) bool {
	return native.SupportsRefresh(m.Manager, c)
}

func (m checkedHomeManager) RefreshActive(ctx context.Context, c native.Captured, checkpoint func(native.Captured) error) (native.Captured, error) {
	if err := checkPrivateAccountHome(m.harness, m.scope, false); err != nil {
		return c, err
	}
	refresher, ok := m.Manager.(native.ActiveRefresher)
	if !ok || !native.SupportsRefresh(m.Manager, c) {
		return c, native.ErrUnsupported
	}
	next, err := refresher.RefreshActive(ctx, c, checkpoint)
	if e := checkPrivateAccountHome(m.harness, m.scope, true); e != nil {
		return next, errors.Join(err, e)
	}
	return next, err
}
