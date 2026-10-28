package runtime

import (
	"os"
	"path/filepath"
)

// Pi reads one agent directory for credentials, settings, MCP servers and
// sessions. Dyna keeps session history shared, and passes settings, MCP
// servers and model definitions per process through Pi's config file flags, so
// the agent directory only follows the account for its login.
const (
	piAgentDirKey   = "PI_CODING_AGENT_DIR"
	piSessionDirKey = "PI_CODING_AGENT_SESSION_DIR"
)

// ensurePiSharedHome creates Pi's shared harness home and its session
// directory. Session history stays there for every account.
func ensurePiSharedHome(home string) error {
	if err := safeAccountPath(home); err != nil {
		return err
	}
	for _, dir := range []string{home, filepath.Join(home, "sessions")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0700); err != nil {
			return err
		}
	}
	return nil
}
