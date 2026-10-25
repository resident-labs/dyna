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

// Check only credential/config/lock paths. Tool sockets and bridges in the
// paired HOME are valid and must never enter the native credential tree walk.
func privateAccountFiles(h, scope string) []string {
	switch h {
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

// checkedCredentialHome accepts the shared harness home itself, or one account
// home next to it, and rejects every other path. A harness that pins credentials
// per account must never read a credential from anywhere else.
func checkedCredentialHome(scope, home string) error {
	if home == scope {
		return nil
	}
	if !pinnedSecretOf(scope, home) {
		return errAccountPath
	}
	return safeAccountPath(home)
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
// Active native scopes stay at scope.
func validateAccountManagerEnv(h, scope string, env map[string]string) error {
	return validateAccountManagerEnvAt(h, scope, scope, env)
}

// validateAccountManagerEnvAt is validateAccountManagerEnv for a harness that
// keeps each account's credentials in that account's own home. credentialHome
// is that home, or scope when the caller works on the scope itself. Pi reads its
// whole agent directory from the account home, but its session history stays in
// scope, which is the shared harness home.
func validateAccountManagerEnvAt(h, scope, credentialHome string, env map[string]string) error {
	if err := safeAccountPath(scope); err != nil {
		return err
	}
	if h == "claude-code" || h == "pi" {
		if err := checkedCredentialHome(scope, credentialHome); err != nil {
			return err
		}
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
		expected["CLAUDE_SECURESTORAGE_CONFIG_DIR"] = credentialHome
		expected["ANTHROPIC_CONFIG_DIR"] = filepath.Join(scope, "anthropic")
	case "grok":
		expected["GROK_HOME"] = scope
	case "pi":
		expected[piAgentDirKey] = credentialHome
		expected[piSessionDirKey] = filepath.Join(scope, "sessions")
	case "kimi-code":
		expected["KIMI_CODE_HOME"] = scope
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
