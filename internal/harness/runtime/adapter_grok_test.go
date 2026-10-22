package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func grokAdapter(t *testing.T) RuntimeAdapter {
	t.Helper()
	a, err := Adapter("grok")
	if err != nil {
		t.Fatalf("Adapter(grok): %v", err)
	}
	return a
}

// TestGrokAdapterShape pins the declarative contract fields to the verified
// native scope: GROK_HOME config.toml with [mcp_servers] and skills/.
func TestGrokAdapterShape(t *testing.T) {
	a := grokAdapter(t)
	if a.Harness != "grok" || a.Binary != "grok" {
		t.Fatalf("Harness/Binary = %q/%q, want grok/grok", a.Harness, a.Binary)
	}
	if a.NPMPackage != "@xai-official/grok" {
		t.Errorf("NPMPackage = %q, want @xai-official/grok", a.NPMPackage)
	}
	if a.ConfigFormat != "toml" || a.MCPKey != "mcp_servers" {
		t.Errorf("ConfigFormat/MCPKey = %q/%q, want toml/mcp_servers", a.ConfigFormat, a.MCPKey)
	}
	if a.ConfigFile != "config.toml" || a.SkillDir != "skills" {
		t.Errorf("ConfigFile/SkillDir = %q/%q, want config.toml/skills", a.ConfigFile, a.SkillDir)
	}
	if a.Exclusive {
		t.Error("Exclusive = true; an exclusive account lease queues every Grok turn behind the one running")
	}
	if a.AccountBlocked != "" {
		t.Errorf("AccountBlocked = %q, want empty; auth.json stays inside GROK_HOME", a.AccountBlocked)
	}
	if a.Install == nil {
		t.Error("Grok must extract its pinned native binary at install time")
	}
}

// TestGrokNativeEnvPrivateScope checks that the environment redirects the
// native scope into the account home, clears inherited provider config, and
// never overrides HOME or XDG.
func TestGrokNativeEnvPrivateScope(t *testing.T) {
	personal := t.TempDir()
	t.Setenv("HOME", personal)
	home := filepath.Join(t.TempDir(), "accounts", "grok", "0123456789abcdef0123456789abcdef")
	env := grokAdapter(t).NativeEnv(home)
	if env["GROK_HOME"] != home {
		t.Errorf("GROK_HOME = %q, want %q", env["GROK_HOME"], home)
	}
	if env["GROK_DISABLE_AUTOUPDATER"] != "1" {
		t.Errorf("GROK_DISABLE_AUTOUPDATER = %q, want 1", env["GROK_DISABLE_AUTOUPDATER"])
	}
	for _, key := range []string{"XAI_API_KEY", "GROK_XAI_API_BASE_URL", "GROK_MODELS_BASE_URL", "GROK_MODELS_LIST_URL", "GROK_DEFAULT_MODEL", "GROK_WEB_SEARCH_MODEL", "GROK_CONFIG", "GROK_CONFIG_PATH"} {
		if value, ok := env[key]; !ok || value != "" {
			t.Errorf("%s = %q (present %v), want cleared", key, value, ok)
		}
	}
	for key, value := range env {
		if key == "HOME" || strings.HasPrefix(key, "XDG_") {
			t.Errorf("NativeEnv overrides %s=%q", key, value)
		}
		if key != "GROK_HOME" && value != "" && value != "1" {
			t.Errorf("NativeEnv value %s=%q leaves the private scope", key, value)
		}
	}
	entries, err := os.ReadDir(personal)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("NativeEnv wrote into personal HOME: %v", entries)
	}
}

// TestGrokConfigArgsPrivateScope checks the launch argv: the leader socket
// stays inside the account home and argv elements keep user values literal.
func TestGrokConfigArgsPrivateScope(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := filepath.Join(t.TempDir(), "acct home", "grok-account")
	args := grokAdapter(t).ConfigArgs(home, "/tmp/generation/config.toml", "/tmp/generation/skills")
	want := []string{"--no-auto-update", "--no-leader", "--leader-socket", filepath.Join(home, "leader.sock")}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("ConfigArgs = %q, want %q", args, want)
	}
	socket := args[3]
	if !strings.HasPrefix(socket, home+string(filepath.Separator)) {
		t.Errorf("leader socket %q leaves the account home %q", socket, home)
	}
	for _, arg := range args {
		if arg == "HOME" || strings.HasPrefix(arg, "XDG_") || strings.HasPrefix(arg, "--home") {
			t.Errorf("ConfigArgs overrides home scope: %q", arg)
		}
	}
}

// TestGrokLoginArgsScoped checks the native login argv: grok login with the
// private leader socket, no credentials, no shell interpolation.
func TestGrokLoginArgsScoped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := filepath.Join(t.TempDir(), "acct home", "grok-account")
	args := grokAdapter(t).LoginArgs("/p/bin/grok", home)
	want := []string{"/p/bin/grok", "--no-leader", "login", "--leader-socket", filepath.Join(home, "leader.sock")}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("LoginArgs = %q, want %q", args, want)
	}
	for _, arg := range args {
		if strings.ContainsAny(arg, ";|&$`<>") {
			t.Errorf("LoginArgs element %q has shell metacharacters", arg)
		}
	}
}

// TestGrokScopePaths checks that every declarative path is a clean relative
// path in the native or real-home scope.
func TestGrokScopePaths(t *testing.T) {
	a := grokAdapter(t)
	check := func(label, path string) {
		t.Helper()
		if filepath.IsAbs(path) || path != filepath.Clean(path) || strings.Contains(path, "..") {
			t.Errorf("%s = %q, want a clean relative path", label, path)
		}
	}
	check("ConfigFile", a.ConfigFile)
	check("SkillDir", a.SkillDir)
	for _, path := range a.AuthFiles {
		check("AuthFiles", path)
	}
	if strings.Join(a.AuthFiles, ",") != "auth.json" {
		t.Errorf("AuthFiles = %q, want [auth.json]", a.AuthFiles)
	}
}

// TestGrokRunsWithDefaults checks that Dyna imports no personal Grok settings.
// The shared native feature policy reports its remaining gaps separately.
func TestGrokRunsWithDefaults(t *testing.T) {
	a := grokAdapter(t)
	if len(a.PersonalConfigFiles) != 0 || len(a.ConfigAllowlist) != 0 {
		t.Fatalf("personal settings = %q, allowlist = %q; Grok runs with its defaults", a.PersonalConfigFiles, a.ConfigAllowlist)
	}
}
