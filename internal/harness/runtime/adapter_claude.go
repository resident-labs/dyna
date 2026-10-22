package runtime

import "path/filepath"

func init() {
	RegisterRuntimeAdapter(RuntimeAdapter{Harness: "claude-code", Binary: "claude", SkillsPinned: true, SkillDir: "skills", AuthFiles: []string{".credentials.json", ".claude.json"}, NativeEnv: func(home string) map[string]string {
		return map[string]string{"CLAUDE_CONFIG_DIR": home, "CLAUDE_SECURESTORAGE_CONFIG_DIR": home, "ANTHROPIC_CONFIG_DIR": filepath.Join(home, "anthropic"), "DISABLE_UPDATES": "1", "FORCE_AUTOUPDATE_PLUGINS": ""}
	}, NPMExecutable: func(root string) string {
		return npmPlatformExecutable(root, "@anthropic-ai/claude-code", "claude", "")
	}})
}
