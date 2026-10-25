package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/profile"
)

func settingsLayer(t *testing.T, argv []string) map[string]any {
	t.Helper()
	found := false
	value := ""
	for i, arg := range argv {
		if arg == "--settings" && i+1 < len(argv) {
			value, found = argv[i+1], true
		}
	}
	if !found {
		t.Fatalf("no --settings layer in %v", argv)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(value), &out); err != nil {
		t.Fatalf("settings layer is not JSON: %s", value)
	}
	return out
}

func TestClaudeLaunchTurnsAllAttributionOff(t *testing.T) {
	inv, err := buildInvocation(profile.Profile{Name: "c", Harness: profile.HarnessClaudeCode, Model: "sonnet"}, "task")
	if err != nil {
		t.Fatal(err)
	}
	for name, argv := range map[string][]string{"initial": inv.initial.argv, "resume": inv.resume("ses", "again").argv} {
		layer := settingsLayer(t, argv)
		if _, exists := layer["includeCoAuthoredBy"]; exists {
			t.Fatalf("%s: includeCoAuthoredBy = %v", name, layer["includeCoAuthoredBy"])
		}
		attr, ok := layer["attribution"].(map[string]any)
		if !ok {
			t.Fatalf("%s: attribution = %v", name, layer["attribution"])
		}
		for key, want := range map[string]any{"commit": "", "pr": "", "sessionUrl": false, "commitTrailers": false} {
			if attr[key] != want {
				t.Fatalf("%s: attribution.%s = %v, want %v", name, key, attr[key], want)
			}
		}
	}
}

func TestClaudeAttributionSettingsMergeOverProfileSettings(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"language":"japanese","attribution":{"commit":"Custom trailer"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	args, err := claudeArgsWithoutAttribution([]string{"--model", "sonnet", "--settings", "settings.json", "--settings", `{"spinnerTipsEnabled":false}`}, dir)
	if err != nil {
		t.Fatal(err)
	}
	layer := settingsLayer(t, args)
	if layer["language"] != "japanese" || layer["spinnerTipsEnabled"] != false {
		t.Fatalf("profile settings lost: %v", layer)
	}
	attr := layer["attribution"].(map[string]any)
	if attr["commit"] != "" {
		t.Fatalf("profile attribution overrode Dyna's off switch: %v", attr)
	}
	if strings.Contains(strings.Join(args, " "), "Custom trailer") {
		t.Fatalf("profile attribution text survived: %v", args)
	}
}

func TestClaudeAttributionResolvesSettingsAgainstHarnessCwd(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "team.json"), []byte(`{"language":"japanese"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	args, err := claudeArgsWithoutAttribution([]string{"--settings", "team.json"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if layer := settingsLayer(t, args); layer["language"] != "japanese" {
		t.Fatalf("settings were not read from the harness cwd: %v", layer)
	}
	if _, err := claudeArgsWithoutAttribution([]string{"--settings", "team.json"}, t.TempDir()); err == nil {
		t.Fatal("unreadable settings passed instead of failing the launch")
	}
}
