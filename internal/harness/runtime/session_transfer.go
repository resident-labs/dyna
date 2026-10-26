package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dyna-agent/internal/mode"
	"dyna-agent/internal/profile"
)

// PiSessionPath returns the Pi session file of id in the shared Pi session
// store. Pi records a session as <time>_<id>.jsonl under the harness home.
func PiSessionPath(id string) (string, error) {
	if id == "" {
		return "", errors.New("pi session id is empty")
	}
	home, err := NewAccounts().HarnessHome(profile.HarnessPi)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read pi sessions: %w", err)
	}
	suffix := "_" + id + ".jsonl"
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), suffix) {
			return filepath.Join(dir, entry.Name()), nil
		}
	}
	return "", fmt.Errorf("pi session %s has no session file", id)
}

// ClaudeHistoryDir resolves the CLAUDE_CONFIG_DIR a turn for p uses, which is
// where Claude keeps resume history. A turn reads that variable from the
// active account's launch environment; today it is the shared harness home
// and the account home holds only credentials (see claude_pin.go). Resolving
// it here means a later per-account history change also reaches a converted
// session. When no account can be resolved yet, the shared home is the answer
// the account manager would use once a sign-in exists.
func ClaudeHistoryDir(ctx context.Context, p profile.Profile) (string, error) {
	if p.Harness != profile.HarnessClaudeCode {
		return "", fmt.Errorf("profile %q does not use Claude Code", p.Name)
	}
	accounts := NewAccounts()
	home, err := accounts.HarnessHome(p.Harness)
	if err != nil {
		return "", err
	}
	provider := ProviderForModel(p.Harness, p.Model)
	active, err := accounts.activeFor(p.Harness, provider, p.Env[mode.EnvRoomID], p.RuntimeRepo)
	if err != nil {
		return home, nil
	}
	env, err := accounts.launchEnv(active.ID)
	if err != nil {
		return home, nil
	}
	if dir := strings.TrimSpace(env["CLAUDE_CONFIG_DIR"]); dir != "" {
		return dir, nil
	}
	return home, nil
}

// InstalledVersion returns the version of the installed harness bundle. The
// session converter pins its writers to known versions and falls back when the
// installed one differs.
func InstalledVersion(harness string) (string, error) {
	bundle, err := New().Bundles.Resolve(harness, "")
	if err != nil {
		return "", err
	}
	if bundle.Version == "" {
		return "", errors.New("harness bundle has no version")
	}
	return bundle.Version, nil
}
