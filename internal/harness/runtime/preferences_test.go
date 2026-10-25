package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	harnessmgr "dyna-agent/internal/harness/config"
	"dyna-agent/internal/harness/preferences"
	"dyna-agent/internal/store"
)

func preferenceFixture(t *testing.T, harness string) (*Runtime, NativeAccount, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "owner")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	r := &Runtime{Accounts: &Accounts{Root: filepath.Join(root, "accounts")}, Config: &harnessmgr.ConfigManager{Path: filepath.Join(root, "config", "dyna", "harnesses.json")}, Generations: filepath.Join(root, "generations")}
	a, err := r.Accounts.Create(harness, "fake private account")
	if err != nil {
		t.Fatal(err)
	}
	return r, a, home
}
func writePreferenceFixture(t *testing.T, home, relative, contents string) string {
	t.Helper()
	path := filepath.Join(home, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func preferenceStatuses(p PreferencePreview) map[string]string {
	out := map[string]string{}
	for _, k := range p.Keys {
		out[k.Key] = k.Status
	}
	return out
}
func TestPreferenceImportClaudeSafeAndImmutable(t *testing.T) {
	r, a, home := preferenceFixture(t, "claude-code")
	source := `{
 // Harmless preferences only.
 "language":"English", "spinnerTipsEnabled":false,"showTurnDuration":true,
 "terminalProgressBarEnabled":true,"prefersReducedMotion":true,
 "newCommand":"echo secret-value", "hooks":{"Stop":[{"command":"echo secret-value"}]},
 "env":{"PRIVATE_TOKEN":"secret-value"},"mcpServers":{"private":{"command":"secret-value"}},
 "permissions":{"defaultMode":"bypassPermissions"},"model":"private-model",
 "unknownSetting":"secret-value", "skills":["secret-value"],
 }`
	path := writePreferenceFixture(t, home, ".claude/settings.json", source)
	before, _ := os.Stat(path)
	p, err := r.PreviewPreferenceImport(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Harness != "claude-code" || p.AccountID != a.ID || len(p.Hash) != 64 {
		t.Fatalf("invalid preview: %#v", p)
	}
	statuses := preferenceStatuses(p)
	for _, k := range []string{"language", "spinnerTipsEnabled", "showTurnDuration", "terminalProgressBarEnabled", "prefersReducedMotion"} {
		if statuses[k] != "allowed" {
			t.Fatalf("%s: %s", k, statuses[k])
		}
	}
	for _, k := range []string{"newCommand", "hooks.Stop", "env.PRIVATE_TOKEN", "mcpServers.private.command", "permissions.defaultMode", "model", "skills"} {
		if statuses[k] != "not imported (unsafe)" {
			t.Fatalf("%s: %s", k, statuses[k])
		}
	}
	if statuses["unknownSetting"] != "not imported (unknown)" {
		t.Fatal(statuses)
	}
	encoded, _ := json.Marshal(p)
	if strings.Contains(string(encoded), "secret-value") || strings.Contains(string(encoded), "English") {
		t.Fatal("preview contains a value")
	}
	if err := r.ImportPreferences(a.ID, p.Hash); err != nil {
		t.Fatal(err)
	}
	values, err := r.PreferenceValues(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 5 || values["language"] != "English" {
		t.Fatal(values)
	}
	ref, ok, err := preferences.For(a.ID)
	if err != nil || !ok {
		t.Fatalf("reference = %+v, %v, %v", ref, ok, err)
	}
	old := ref.Settings
	raw, _ := os.ReadFile(old)
	for path, mode := range map[string]os.FileMode{old: 0600, filepath.Dir(old): 0700, filepath.Dir(filepath.Dir(old)): 0700} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("mode %s: %v", path, err)
		}
	}
	writePreferenceFixture(t, home, ".claude/settings.json", `{"language":"French"}`)
	p2, err := r.PreviewPreferenceImport(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ImportPreferences(a.ID, p2.Hash); err != nil {
		t.Fatal(err)
	}
	ref, _, err = preferences.For(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Settings == old {
		t.Fatal("reimport reused layer")
	}
	oldRaw, _ := os.ReadFile(old)
	if string(raw) != string(oldRaw) {
		t.Fatal("old layer changed")
	}
	// Import must leave the owner file exactly as supplied and the account home empty.
	ownerRaw, _ := os.ReadFile(path)
	if string(ownerRaw) != `{"language":"French"}` {
		t.Fatal("owner file changed")
	}
	entries, err := os.ReadDir(a.Home)
	if err != nil || len(entries) != 0 {
		t.Fatal("account home was written")
	}
	if before.Mode().Perm() != 0600 {
		t.Fatal("fixture mode")
	}
	metaRaw, _ := json.Marshal(ref)
	if strings.Contains(string(metaRaw), "French") || strings.Contains(string(metaRaw), "secret-value") {
		t.Fatal("metadata contains values")
	}
}
func TestPreferenceUnsafeValuesAndTypes(t *testing.T) {
	cases := []string{`"~/private"`, `"/home/private"`, `"C:\\Users\\private"`, `"https://user:pass@example.com"`, `"$(echo private)"`, `"English; echo private"`, `"sk-private"`, `"eyJhbGciOiJIUzI1NiJ9"`, `["English"]`, `{"command":"private"}`, `42`, `null`}
	for _, value := range cases {
		t.Run(value, func(t *testing.T) {
			r, a, home := preferenceFixture(t, "claude-code")
			writePreferenceFixture(t, home, ".claude/settings.json", `{"language":`+value+`}`)
			p, err := r.PreviewPreferenceImport(a.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range p.Keys {
				if key.Status == "allowed" {
					t.Fatal("unsafe value accepted")
				}
			}
			if err := r.ImportPreferences(a.ID, p.Hash); err == nil {
				t.Fatal("empty import succeeded")
			}
			if _, err := os.Stat(r.Generations); !os.IsNotExist(err) {
				t.Fatal("empty import created generation")
			}
		})
	}
}
func TestPreferenceErrorsDoNotExposeContents(t *testing.T) {
	r, a, home := preferenceFixture(t, "muse")
	adapters.Lock()
	original := adapters.items["muse"]
	modified := original
	modified.PersonalConfigFiles = []string{"prefs/config.toml"}
	adapters.items["muse"] = modified
	adapters.Unlock()
	defer func() { adapters.Lock(); adapters.items["muse"] = original; adapters.Unlock() }()
	writePreferenceFixture(t, home, "prefs/config.toml", `secret-value = "unterminated`)
	p, err := r.PreviewPreferenceImport(a.ID)
	if err == nil {
		t.Fatal("invalid TOML accepted")
	}
	encoded, _ := json.Marshal(p)
	if strings.Contains(string(encoded), "secret-value") || strings.Contains(err.Error(), "secret-value") || strings.Contains(err.Error(), home) {
		t.Fatal("error disclosed source")
	}
}
func TestPreferenceSourceSymlinkRefusal(t *testing.T) {
	r, a, home := preferenceFixture(t, "claude-code")
	outside := writePreferenceFixture(t, t.TempDir(), "outside.json", `{"language":"English"}`)
	if err := os.Mkdir(filepath.Join(home, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, ".claude/settings.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PreviewPreferenceImport(a.ID); err == nil {
		t.Fatal("symlink source accepted")
	}
}
func TestPreferenceDenylistOverridesAdapterAndNoAllowlist(t *testing.T) {
	r, a, home := preferenceFixture(t, "muse")
	adapters.Lock()
	original := adapters.items["muse"]
	modified := original
	modified.ConfigAllowlist = []string{"hooks.Stop", "env.API_KEY", "mcp_servers.example.command", "model", "provider", "permissions", "approval_policy", "sandbox_mode", "status_line.command", "notification.command", "personal_path", "safe_unknown"}
	modified.PersonalConfigFiles = []string{"prefs/config.jsonc"}
	adapters.items["muse"] = modified
	adapters.Unlock()
	defer func() { adapters.Lock(); adapters.items["muse"] = original; adapters.Unlock() }()
	writePreferenceFixture(t, home, "prefs/config.jsonc", `{"hooks":{"Stop":true},"env":{"API_KEY":true},"mcp_servers":{"example":{"command":true}},"model":true,"provider":true,"permissions":true,"approval_policy":true,"sandbox_mode":true,"status_line":{"command":true},"notification":{"command":true},"personal_path":true,"safe_unknown":true}`)
	p, err := r.PreviewPreferenceImport(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range p.Keys {
		if key.Key != "safe_unknown" && key.Status != "not imported (unsafe)" {
			t.Fatalf("denylist failed: %#v", key)
		}
	}
	adapters.Lock()
	modified.ConfigAllowlist = []string{}
	adapters.items["muse"] = modified
	adapters.Unlock()
	p, err = r.PreviewPreferenceImport(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preferenceStatuses(p)["safe_unknown"] != "not imported (unknown)" {
		t.Fatal(p)
	}
	if err := r.ImportPreferences(a.ID, p.Hash); err == nil {
		t.Fatal("empty allowlist imported settings")
	}
}
func TestPreferenceValuesRevalidateAndConfineReference(t *testing.T) {
	r, a, home := preferenceFixture(t, "claude-code")
	writePreferenceFixture(t, home, ".claude/settings.json", `{"language":"English"}`)
	p, err := r.PreviewPreferenceImport(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ImportPreferences(a.ID, p.Hash); err != nil {
		t.Fatal(err)
	}
	ref, ok, err := preferences.For(a.ID)
	if err != nil || !ok {
		t.Fatalf("reference = %+v, %v, %v", ref, ok, err)
	}
	if err := store.WriteJSON(ref.Settings, map[string]any{"language": "~/private"}, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PreferenceValues(a.ID); err == nil {
		t.Fatal("unsafe stored preference accepted")
	}
	ref.Settings = filepath.Join(home, ".claude/settings.json")
	if err := preferences.Save(a.ID, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PreferenceValues(a.ID); err == nil {
		t.Fatal("outside layer reference accepted")
	}
}
func TestPreferenceFlattenCollisionAndNoArrays(t *testing.T) {
	_, err := flattenPreferences(map[string]any{"a.b": true, "a": map[string]any{"b": false}})
	if err == nil {
		t.Fatal("ambiguous dotted keys accepted")
	}
	flat, err := flattenPreferences(map[string]any{"editor.fontSize": float64(12), "nested": map[string]any{"value": true}})
	if err != nil || !reflect.DeepEqual(flat, map[string]any{"editor.fontSize": float64(12), "nested.value": true}) {
		t.Fatalf("%v %v", flat, err)
	}
}

func TestPreferenceMissingSourceAndAllowlistHash(t *testing.T) {
	r, a, home := preferenceFixture(t, "muse")
	p, err := r.PreviewPreferenceImport(a.ID)
	if err != nil || len(p.Keys) != 0 {
		t.Fatalf("%v %v", p, err)
	}
	if err := r.ImportPreferences(a.ID, p.Hash); err == nil {
		t.Fatal("missing source imported")
	}
	writePreferenceFixture(t, home, "prefs/config.toml", "hide_agent_reasoning = true\n")
	p, err = r.PreviewPreferenceImport(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	adapters.Lock()
	original := adapters.items["muse"]
	modified := original
	modified.ConfigAllowlist = []string{"history.persistence"}
	adapters.items["muse"] = modified
	adapters.Unlock()
	defer func() { adapters.Lock(); adapters.items["muse"] = original; adapters.Unlock() }()
	if err := r.ImportPreferences(a.ID, p.Hash); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("allowlist change was not refused: %v", err)
	}
}
func TestPreferenceInvalidSourceSelection(t *testing.T) {
	r, a, home := preferenceFixture(t, "muse")
	writePreferenceFixture(t, home, "prefs/config.toml", "hide_agent_reasoning = true\n")
	adapters.Lock()
	original := adapters.items["muse"]
	adapters.Unlock()
	defer func() { adapters.Lock(); adapters.items["muse"] = original; adapters.Unlock() }()
	for _, path := range []string{"../outside.toml", "/outside.toml", "prefs/../outside.toml"} {
		adapters.Lock()
		modified := original
		modified.PersonalConfigFiles = []string{path}
		adapters.items["muse"] = modified
		adapters.Unlock()
		if _, err := r.PreviewPreferenceImport(a.ID); err == nil {
			t.Fatal("unsafe source path accepted")
		}
	}
}
