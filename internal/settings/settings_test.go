package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/profile"
)

func TestLoadMissingUsesDefaults(t *testing.T) {
	v, err := Load(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !v.AutoCheck || v.WorktreesRetention != WorktreesRetentionChanged {
		t.Fatalf("defaults = %#v", v)
	}

}

func TestLoadKeepsExactIntegerSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	// 9007199254740993 is the smallest integer above float64's exact range.
	raw := `{"version": 1, "worktrees": {"minFreeBytes": 9007199254740993}}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if v.WorktreesMinFreeBytes != 9007199254740993 {
		t.Fatalf("minFreeBytes = %d, want 9007199254740993", v.WorktreesMinFreeBytes)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), "9007199254740993") {
		t.Fatalf("saved settings lost the exact number:\n%s", saved)
	}
}

// loadSettingsFile writes one settings file and loads it, so each case states
// only the JSON it is about.
func loadSettingsFile(t *testing.T, raw string) (Values, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestLoadRefusesTrailingInput(t *testing.T) {
	for _, raw := range []string{
		`{"version": 1} {"version": 1}`,
		`{"version": 1} garbage`,
	} {
		if _, err := loadSettingsFile(t, raw); err == nil {
			t.Errorf("trailing input accepted: %s", raw)
		}
	}
	if _, err := loadSettingsFile(t, "{\"version\": 1}\n\n"); err != nil {
		t.Errorf("trailing whitespace refused: %v", err)
	}
}

func TestLoadAcceptsIntegralNumberForms(t *testing.T) {
	v, err := loadSettingsFile(t, `{"version": 1, "worktrees": {"minFreeBytes": 1e9}}`)
	if err != nil || v.WorktreesMinFreeBytes != 1000000000 {
		t.Fatalf("1e9 bytes = %d, %v", v.WorktreesMinFreeBytes, err)
	}
	v, err = loadSettingsFile(t, `{"version": 1, "subagent": {"maxDepth": 4.0}}`)
	if err != nil || v.SubagentMaxDepth != 4 {
		t.Fatalf("4.0 depth = %d, %v", v.SubagentMaxDepth, err)
	}
	if _, err = loadSettingsFile(t, `{"version": 9.0}`); err == nil || !strings.Contains(err.Error(), "unsupported settings version") {
		t.Fatalf("9.0 version = %v", err)
	}
	if _, err = loadSettingsFile(t, `{"version": 1, "worktrees": {"minFreeBytes": 4.5}}`); err == nil {
		t.Fatal("a fractional byte count was accepted")
	}
	// The known integer consumers read an integral JSON number the same way.
	v, err = loadSettingsFile(t, `{"version": 1, "library": {"readmeMaxLines": 3e2}, "runs": {"retention": 0.0}}`)
	if err != nil || v.LibraryReadmeMaxLines != 300 || v.RunsRetention != 0 {
		t.Fatalf("integral forms = %d lines, %v retention, %v", v.LibraryReadmeMaxLines, v.RunsRetention, err)
	}
	if _, err = loadSettingsFile(t, `{"version": 1, "library": {"readmeMaxLines": 2.5}}`); err == nil {
		t.Error("a fractional line count was accepted")
	}
	if _, err = loadSettingsFile(t, `{"version": 1, "runs": {"retention": 1.5}}`); err == nil {
		t.Error("a fractional retention was accepted")
	}
}

// A present version must name the one schema this build reads. A version that
// cannot be read as an integer used to skip the check and was then overwritten
// by the next save.
func TestLoadRefusesAVersionItCannotRead(t *testing.T) {
	for _, raw := range []string{
		`{"version": 1e309}`,
		`{"version": 1.5}`,
		`{"version": "1"}`,
	} {
		if _, err := loadSettingsFile(t, raw); err == nil {
			t.Errorf("broken version accepted: %s", raw)
		}
	}
	if _, err := loadSettingsFile(t, `{}`); err != nil {
		t.Errorf("a file with no version was refused: %v", err)
	}
}

func TestSavePreservesUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := `{
  "version": 1,
  "ui": {"defaultTab": "workflows", "theme": "dim"},
  "future": {"flag": true},
  "admission": {"maxAgents": 99, "note": "keep"},
  "checks": {"host": "unused"},
  "notifications": {"future": true},
  "worktrees": {"keep": "future"},
  "voice": {"speechToSpeechURL": "ws://unused/v1/realtime"}
}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := Load(path)
	if err != nil {
		t.Fatalf("Load() = %#v %v", v, err)
	}
	v.AutoCheck = false
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	ui := got["ui"].(map[string]any)
	if ui["defaultTab"] != "workflows" || ui["theme"] != "dim" {
		t.Fatalf("ui = %#v", ui)
	}
	if got["future"].(map[string]any)["flag"] != true {
		t.Fatalf("future key dropped: %s", b)
	}
	if adm, ok := got["admission"].(map[string]any); !ok || adm["note"] != "keep" {
		t.Fatalf("admission note dropped: %s", b)
	}
	if got["admission"].(map[string]any)["maxAgents"] != float64(99) {
		t.Fatalf("unknown legacy settings changed: %s", b)
	}
	for section, key := range map[string]string{
		"checks": "host", "notifications": "future",
		"worktrees": "keep", "voice": "speechToSpeechURL",
	} {
		values, ok := got[section].(map[string]any)
		if !ok || values[key] == nil {
			t.Errorf("unknown %s.%s dropped: %s", section, key, b)
		}
	}
	if got["updater"].(map[string]any)["autoCheck"] != false {
		t.Fatalf("autoCheck not saved: %s", b)
	}
}

func TestUnknownVersionFailsSafely(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"version":9,"ui":{"defaultTab":"settings"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("unknown version err = %v", err)
	}
}

func TestEnvWinsOverAutoCheck(t *testing.T) {
	v := Defaults()
	v.AutoCheck = true
	t.Setenv("DYNA_NO_AUTO_UPDATE", "1")
	if v.EffectiveAutoCheck() || v.Source(KeyAutoCheck) != SourceEnv {
		t.Fatalf("env override = %v %s", v.EffectiveAutoCheck(), v.Source(KeyAutoCheck))
	}
}

func TestSetValidatesKeys(t *testing.T) {
	v := Defaults()
	if err := v.Set("ui.defaultTab", "profiles"); err == nil {
		t.Fatal("retired setting accepted")
	}
	if err := v.Set("admission.maxAgents", "8"); err == nil {
		t.Fatal("admission key stored in settings")
	}
	if err := v.Set("ui.theme", "dark"); err == nil {
		t.Fatal("unknown key accepted")
	}
	for _, retention := range []string{WorktreesRetentionChanged, WorktreesRetentionAll, WorktreesRetentionNone} {
		if err := v.Set(KeyWorktreesRetention, retention); err != nil || v.WorktreesRetention != retention {
			t.Fatalf("Set(%q) = %q, %v", retention, v.WorktreesRetention, err)
		}
	}
	if err := v.Set(KeyWorktreesRetention, "sometimes"); err == nil {
		t.Fatal("invalid worktree retention accepted")
	}
}

func TestWorktreesRetentionRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	v := Defaults()
	v.path = path
	if err := v.Set(KeyWorktreesRetention, WorktreesRetentionAll); err != nil {
		t.Fatal(err)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || got.WorktreesRetention != WorktreesRetentionAll || got.Source(KeyWorktreesRetention) != SourceFile {
		t.Fatalf("Load() = %#v, %v", got, err)
	}
}

func TestInvalidStoredWorktreesRetentionUsesDefaultSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"worktrees":{"retention":"sometimes"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if v.WorktreesRetention != WorktreesRetentionChanged || v.Source(KeyWorktreesRetention) != SourceDefault {
		t.Fatalf("invalid stored retention = %q from %q", v.WorktreesRetention, v.Source(KeyWorktreesRetention))
	}
}

func TestAtomicSaveMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	v := Defaults()
	v.path = path
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v %v", info.Mode(), err)
	}
}

func TestSubagentAndSteeringSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	v, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if v.SubagentMaxDepth != 3 || v.SteeringMaxToolWait != 120*time.Second {
		t.Fatalf("defaults = %d, %s", v.SubagentMaxDepth, v.SteeringMaxToolWait)
	}
	for _, bad := range [][2]string{
		{KeySubagentMaxDepth, "0"}, {KeySubagentMaxDepth, "x"},
		{KeySteeringToolWait, "0"}, {KeySteeringToolWait, "-5s"}, {KeySteeringToolWait, "soon"},
	} {
		if err := v.Set(bad[0], bad[1]); err == nil {
			t.Fatalf("Set(%s, %s) accepted", bad[0], bad[1])
		}
	}
	if err := v.Set(KeySubagentMaxDepth, "5"); err != nil {
		t.Fatal(err)
	}
	if err := v.Set(KeySteeringToolWait, "45"); err != nil { // bare seconds
		t.Fatal(err)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.SubagentMaxDepth != 5 || again.SteeringMaxToolWait != 45*time.Second ||
		again.Source(KeySubagentMaxDepth) != SourceFile || again.Source(KeySteeringToolWait) != SourceFile {
		t.Fatalf("round trip = %d, %s", again.SubagentMaxDepth, again.SteeringMaxToolWait)
	}
}

func TestDefaultPathUsesConfigDirectory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, config := range []string{t.TempDir(), ""} {
		t.Setenv("XDG_CONFIG_HOME", config)
		// Profile is a test-only reference to the path used before this change.
		want := filepath.Join(filepath.Dir(profile.DefaultPath()), "settings.json")
		if got := DefaultPath(); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

func TestIdleUnloadMinutesSettingValidatesAndConverts(t *testing.T) {
	v := Defaults()
	if v.HarnessIdleUnload() != 10*time.Minute {
		t.Fatalf("default %v", v.HarnessIdleUnload())
	}
	for _, bad := range []string{"-1", "x", "1.5", ""} {
		if err := v.Set(KeyIdleUnloadMinutes, bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	if err := v.Set(KeyIdleUnloadMinutes, "0"); err != nil || v.HarnessIdleUnload() >= 0 {
		t.Fatalf("0 must mean never unload (negative): %v, %v", err, v.HarnessIdleUnload())
	}
	if err := v.Set(KeyIdleUnloadMinutes, "30"); err != nil || v.HarnessIdleUnload() != 30*time.Minute || v.Source(KeyIdleUnloadMinutes) != SourceFile {
		t.Fatalf("30: %v, %v", err, v.HarnessIdleUnload())
	}
}

func TestRulesMaxCharsSetting(t *testing.T) {
	v, err := loadSettingsFile(t, `{"version": 1, "rules": {"maxChars": 4e3}}`)
	if err != nil || v.RulesMaxChars != 4000 || v.Source(KeyRulesMaxChars) != SourceFile {
		t.Fatal(v.RulesMaxChars, err)
	}
	if _, err := loadSettingsFile(t, `{"rules": {"maxChars": 499}}`); err == nil {
		t.Fatal("cap below one longest rule accepted")
	}
	if err := v.Set(KeyRulesMaxChars, "2.5"); err == nil {
		t.Fatal("fractional cap accepted")
	}
}
