package runtime

import (
	"bytes"
	"os"
	"path/filepath"

	"dyna-agent/internal/fsutil"
)

// Pi reads one agent directory for credentials, settings, MCP servers and
// sessions. Dyna keeps session history shared, so only the agent directory
// follows the account: the account home holds that account's own login.
const (
	piAgentDirKey    = "PI_CODING_AGENT_DIR"
	piSessionDirKey  = "PI_CODING_AGENT_SESSION_DIR"
	piGenerationMark = ".dyna-generation"
)

// publishPiAccountHome gives a Pi account home the parts of the agent directory
// that Dyna manages: the generation's settings and MCP servers and the owner's
// model definitions. Pi must find them next to the credential, because it reads
// one agent directory. Pi's own files (its login and its model cache) are never
// touched, and the shared home stays the source of models.json.
func publishPiAccountHome(dir, home, accountHome string) error {
	if err := checkedCredentialHome(home, accountHome); err != nil {
		return err
	}
	if err := copyPiModels(home, accountHome); err != nil {
		return err
	}
	// The marker keeps a turn from rewriting views that this generation already
	// published into the account home.
	marker := filepath.Join(accountHome, piGenerationMark)
	if current, err := os.ReadFile(marker); err == nil && string(current) == dir+"\n" {
		return nil
	}
	if err := publishGenerationFile(dir, "settings.json", accountHome, "settings.json"); err != nil {
		return err
	}
	if err := publishGenerationFile(dir, "pi-mcp.json", accountHome, "mcp.json"); err != nil {
		return err
	}
	return fsutil.WriteFile(marker, []byte(dir+"\n"), 0600, false)
}

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

// copyPiModels keeps an account home's model definitions equal to the owner's
// shared file, so a model the owner adds reaches every Pi account. A shared file
// that is absent leaves the account home as it is.
func copyPiModels(home, accountHome string) error {
	raw, err := os.ReadFile(filepath.Join(home, "models.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	target := filepath.Join(accountHome, "models.json")
	if current, err := os.ReadFile(target); err == nil && bytes.Equal(current, raw) {
		return nil
	}
	return fsutil.WriteFile(target, raw, 0600, false)
}
