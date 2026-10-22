package home

import "path/filepath"

// Discovery replaces inherited compatibility roots that can discover personal
// settings, skills, or MCP servers even after HOME changes. Apply these defaults
// before the active adapter's NativeEnv and generation values. Prepare does not
// apply them: the final private HOME merge must preserve active native roots.
// Roots remains limited to HOME and the four XDG settings roots.
// OpenCodeEmptyConfigContent clears an inherited OPENCODE_CONFIG_CONTENT layer.
// OpenCode 2.0.32 logs "rejected malformed JSON or JSONC document" for an
// empty value on every start, so use an empty JSON object instead.
const OpenCodeEmptyConfigContent = "{}"

func Discovery(nativeScope string) map[string]string {
	h := Home(nativeScope)
	codex := filepath.Join(h, ".codex")
	claude := filepath.Join(h, ".claude")
	config := filepath.Join(h, ".config")
	return map[string]string{
		"CODEX_HOME": codex, "CODEX_SQLITE_HOME": filepath.Join(codex, "sqlite"),
		"CLAUDE_CONFIG_DIR": claude, "CLAUDE_SECURESTORAGE_CONFIG_DIR": claude, "ANTHROPIC_CONFIG_DIR": filepath.Join(claude, "anthropic"),
		"GROK_HOME": filepath.Join(h, ".grok"), "PI_CODING_AGENT_DIR": filepath.Join(h, ".pi/agent"),
		"OPENCODE_CONFIG_DIR": filepath.Join(config, "opencode"), "OPENCODE_DB": filepath.Join(h, ".local/share/opencode/opencode.db"),
		"MUSE_AUTH_PATH": filepath.Join(config, "muse/auth.json"),
		"GROK_CONFIG":    "", "GROK_CONFIG_PATH": "", "OPENCODE_CONFIG": "", "OPENCODE_CONFIG_CONTENT": OpenCodeEmptyConfigContent,
		// OpenCode 2.0.32 exits when OPENCODE_TEST_HOME is set but empty.
		"OPENCODE_TEST_HOME": h,
	}
}
