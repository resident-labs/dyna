package cliproxy

import "github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"

// EnableDynaMemoryOAuthCallbacks avoids callback files for Claude and Codex.
func EnableDynaMemoryOAuthCallbacks() {
	management.DynaMemoryOAuthCallbacks = true
}
