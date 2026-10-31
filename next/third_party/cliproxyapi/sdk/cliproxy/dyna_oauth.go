package cliproxy

import "github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"

// SetDynaOAuthCompletionHook sets the private completion handoff before startup.
func SetDynaOAuthCompletionHook(hook func(state, authID string)) {
	management.DynaOAuthCompletionHook = hook
}

// EnableDynaMemoryOAuthCallbacks avoids callback files for Claude and Codex.
func EnableDynaMemoryOAuthCallbacks() {
	management.DynaMemoryOAuthCallbacks = true
}
