package runtime

import "path/filepath"

func init() {
	RegisterRuntimeAdapter(RuntimeAdapter{Harness: "codex", Binary: "codex", SkillDir: "skills", AuthFiles: []string{"auth.json"}, NativeEnv: func(home string) map[string]string {
		return map[string]string{"CODEX_HOME": home, "CODEX_SQLITE_HOME": filepath.Join(home, "sqlite")}
	}})
}
