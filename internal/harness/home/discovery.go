package home

import "path/filepath"

// Discovery replaces inherited compatibility roots that can discover personal
// settings, skills, or MCP servers even after HOME changes. Apply these defaults
// before the active adapter's NativeEnv and generation values. Prepare does not
// apply them: the final private HOME merge must preserve active native roots.
// Roots remains limited to HOME and the four XDG settings roots.

func Discovery(nativeScope string) map[string]string {
	h := Home(nativeScope)
	claude := filepath.Join(h, ".claude")
	return map[string]string{
		"CLAUDE_CONFIG_DIR": claude, "CLAUDE_SECURESTORAGE_CONFIG_DIR": claude, "ANTHROPIC_CONFIG_DIR": filepath.Join(claude, "anthropic"),
		"GROK_HOME": filepath.Join(h, ".grok"), "PI_CODING_AGENT_DIR": filepath.Join(h, ".pi/agent"),
		"GROK_CONFIG": "", "GROK_CONFIG_PATH": "",
	}
}
