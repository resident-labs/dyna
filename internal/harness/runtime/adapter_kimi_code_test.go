package runtime

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestKimiCodeAdapterUsesPrivateManagedBundle(t *testing.T) {
	a, err := Adapter("kimi-code")
	if err != nil {
		t.Fatal(err)
	}
	if a.Harness != "kimi-code" || a.Binary != "kimi" || a.NPMPackage != "@moonshot-ai/kimi-code" {
		t.Fatalf("unexpected Kimi adapter identity: %+v", a)
	}
	if a.ConfigFile != "" {
		t.Fatalf("ConfigFile = %q; native config.toml must never be replaced by a generation", a.ConfigFile)
	}
	if a.ConfigFormat != "json" || a.MCPKey != "mcpServers" || a.SkillDir != "skills" {
		t.Fatalf("unexpected ACP catalog shape: %+v", a)
	}
	home := filepath.Join(t.TempDir(), "account")
	env := a.NativeEnv(home)
	if env["KIMI_CODE_HOME"] != home || env["KIMI_CODE_NO_AUTO_UPDATE"] != "1" || env["KIMI_DISABLE_TELEMETRY"] != "1" {
		t.Fatalf("native environment = %+v", env)
	}
	for _, key := range []string{"KIMI_CODE_BASE_URL", "KIMI_CODE_OAUTH_HOST", "KIMI_MODEL_API_KEY", "KIMI_CODE_CUSTOM_HEADERS"} {
		found := false
		for _, candidate := range a.UnsetEnv {
			found = found || candidate == key
		}
		if !found {
			t.Errorf("%s is not removed from the inherited environment", key)
		}
	}
	filtered := NativeEnvironmentBase("kimi-code", []string{"HOME=/owner", "KIMI_CODE_BASE_URL=https://personal.invalid", "KIMI_MODEL_API_KEY=fixture-private"})
	if !reflect.DeepEqual(filtered, []string{"HOME=/owner"}) {
		t.Fatalf("inherited environment was not removed: %q", filtered)
	}
	wantArgs := []string{"--add-dir", filepath.Join(t.TempDir(), "generation")}
	gotArgs := a.ConfigArgs(home, filepath.Join(wantArgs[1], "settings.json"), filepath.Join(wantArgs[1], "skills"))
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("ConfigArgs = %q, want %q", gotArgs, wantArgs)
	}
}
