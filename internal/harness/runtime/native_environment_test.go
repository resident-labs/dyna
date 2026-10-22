package runtime

import (
	"path/filepath"
	"testing"

	accountnative "dyna-agent/internal/account/native"
)

// nativePathKeys lists the credential path keys each adapter must declare.
var nativePathKeys = map[string][]string{
	"claude-code": {"CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR", "ANTHROPIC_CONFIG_DIR"},
	"codex":       {"CODEX_HOME", "CODEX_SQLITE_HOME"},
	"opencode":    {"OPENCODE_CONFIG_DIR", "OPENCODE_DB"},
	"muse":        {"MUSE_AUTH_PATH", "TBH_CREDENTIAL_BACKEND"},
}

func TestNativeEnvironmentIsSharedByLoginAndRuntime(t *testing.T) {
	for _, h := range []string{"claude-code", "codex", "opencode", "muse", "grok", "pi", "antigravity"} {
		t.Run(h, func(t *testing.T) {
			a := testAccounts(t)
			scope := filepath.Join(a.Root, h, ".runtime")
			state := &sharedLoginFixture{}
			a.NativeManager = func(h string, _ accountnative.Host) (accountnative.Manager, bool) {
				return sharedLoginManager{h, state}, true
			}
			adapter, err := Adapter(h)
			if err != nil {
				t.Fatal(err)
			}
			native := adapter.NativeEnv(scope)
			for _, key := range nativePathKeys[h] {
				if _, ok := native[key]; !ok {
					t.Fatalf("%s is missing from the shared native environment", key)
				}
			}
			_, login, err := a.scopedManager(h, scope, "/fake/native")
			if err != nil {
				t.Fatal(err)
			}
			for key, value := range native {
				if login[key] != value {
					t.Fatalf("login and runtime disagree on %s: %q != %q", key, login[key], value)
				}
			}
		})
	}
}
