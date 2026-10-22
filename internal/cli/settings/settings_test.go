package settings

import (
	"bytes"
	"encoding/json"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/configsvc"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store/storetest"
)

func TestSettingsListJSON(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var got listing
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("list JSON = %q: %v", out.String(), err)
	}
	if got.V != 1 || got.Items == nil {
		t.Fatalf("listing = %#v", got)
	}
	// RawMessage distinguishes an omitted value from an explicit JSON null.
	var wire struct {
		Items []struct {
			Value json.RawMessage `json:"value"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	keys := map[string]listItem{}
	for i, item := range got.Items {
		if item.Key == "" || len(wire.Items[i].Value) == 0 || item.Source == "" || item.LiveVsRestart == "" {
			t.Fatalf("incomplete settings item = %#v", item)
		}
		if _, exists := keys[item.Key]; exists {
			t.Fatalf("duplicate settings key %q", item.Key)
		}
		keys[item.Key] = item
	}
	for _, key := range []string{
		settings.KeyComputersEnabled,
		settings.KeyGoalKeeperTimeout, settings.KeyGoalKeeperProfile,
		settings.KeyMaxCardBytes, settings.KeyMaxFileBytes, settings.KeyAutoCheck,
		settings.KeyRunsRetention, settings.KeyWorktreesRetention, settings.KeyLocalCIRemote, settings.KeyLocalLaneRepos,
		settings.KeyWorktreesRoot, settings.KeyWorktreesMinFreeBytes, settings.KeyRootProfile,
		settings.KeySubagentMaxDepth, settings.KeySteeringToolWait,
		settings.KeyBrowserPath, settings.KeyBrowserExtraFlags, settings.KeyBrowserUseFlagsFile,
		settings.KeyBrowserProfileMode, settings.KeyBrowserOwnerProfile, settings.KeyBrowserSaveBack,
		settings.KeyVoiceProvider, settings.KeyVoiceModel, settings.KeyVoiceURL,
	} {
		if _, exists := keys[key]; !exists {
			t.Errorf("missing settings key %q", key)
		}
	}
	lane := keys[settings.KeyLocalLaneRepos]
	if lane.Source != settings.SourceDefault || lane.LiveVsRestart != settings.LiveNextOperation {
		t.Fatalf("local lane repositories = %#v", lane)
	}
	if keys[settings.KeyGoalKeeperTimeout].Value != "5m0s" || keys[settings.KeyGoalKeeperTimeout].Display != "5m" {
		t.Fatalf("keeper timeout = %#v", keys[settings.KeyGoalKeeperTimeout])
	}
	if keys[settings.KeyBrowserProfileMode].Value != "shared" {
		t.Fatalf("browser profile mode = %#v", keys[settings.KeyBrowserProfileMode])
	}
	if _, ok := keys["ui.defaultTab"]; ok {
		t.Fatal("retired setting is still listed")
	}
	if keys[settings.KeySubagentMaxDepth].Value != float64(3) || keys[settings.KeySteeringToolWait].Value != "2m0s" {
		t.Fatalf("subagent/steering defaults = %#v %#v", keys[settings.KeySubagentMaxDepth], keys[settings.KeySteeringToolWait])
	}
	for _, key := range []string{settings.KeyMaxCardBytes, settings.KeyMaxFileBytes} {
		if keys[key].Value != float64(25<<20) || keys[key].Display != "25 MiB" {
			t.Fatalf("size JSON = %#v", keys[key])
		}
	}
	if keys[settings.KeyRunsRetention].Display != "14d" || keys[settings.KeyRunsRetention].Source != settings.SourceDefault {
		t.Fatalf("run retention = %#v", keys[settings.KeyRunsRetention])
	}
	if keys[settings.KeyWorktreesRetention].Value != settings.WorktreesRetentionChanged || keys[settings.KeyWorktreesRetention].Source != settings.SourceDefault {
		t.Fatalf("worktree retention = %#v", keys[settings.KeyWorktreesRetention])
	}
}

func TestSettingsSetAndGetWorktreesRetention(t *testing.T) {
	storetest.Isolate(t)
	serveSetSetting(t, authority.Actor{Role: authority.Owner}, configsvc.Set)
	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"set", settings.KeyWorktreesRetention, settings.WorktreesRetentionNone})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	cmd = NewCommand()
	out.Reset()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"get", settings.KeyWorktreesRetention, "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var item listItem
	if err := json.Unmarshal(out.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if item.Value != settings.WorktreesRetentionNone || item.Source != settings.SourceFile || item.LiveVsRestart != settings.LiveNextOpen {
		t.Fatalf("worktree setting = %#v", item)
	}
}

func TestRemovedAdmissionKeysAreUnknown(t *testing.T) {
	storetest.Isolate(t)
	serveSetSetting(t, authority.Actor{Role: authority.Owner}, configsvc.Set)
	for _, key := range []string{"admission.maxAgents", "admission.capacityOverride", "ui.defaultTab"} {
		for _, args := range [][]string{{"get", key}, {"set", key, "8"}} {
			cmd := NewCommand()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(args)
			if err := cmd.Execute(); err == nil {
				t.Fatalf("removed key accepted: %v", args)
			}
		}
	}
}

func TestHumanSizes(t *testing.T) {
	storetest.Isolate(t)
	serveSetSetting(t, authority.Actor{Role: authority.Owner}, configsvc.Set)
	run := func(args ...string) (string, error) {
		cmd := NewCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	for _, key := range []string{settings.KeyMaxCardBytes, settings.KeyMaxFileBytes} {
		for _, input := range []string{"25MiB", "25 MiB", "25MB", "26214400", "25m", "26,214,400 bytes"} {
			out, err := run("set", key, input)
			if err != nil || out != "set "+key+" = 25 MiB\n" {
				t.Fatalf("set %s: %q %v", input, out, err)
			}
			out, err = run("get", key)
			if err != nil || out != "25 MiB\n" {
				t.Fatalf("get: %q %v", out, err)
			}
		}
		out, err := run("get", key, "--json")
		if err != nil {
			t.Fatal(err)
		}
		var item listItem
		if err := json.Unmarshal([]byte(out), &item); err != nil {
			t.Fatal(err)
		}
		if item.Value != float64(25<<20) || item.Display != "25 MiB" {
			t.Fatalf("JSON changed: %#v", item)
		}
		for _, input := range []string{"0", "-1", "garbage"} {
			if _, err := run("set", key, input); err == nil {
				t.Fatalf("accepted %s", input)
			}
		}
	}
	if out, err := run("get", settings.KeySteeringToolWait); err != nil || out != "2m\n" {
		t.Fatalf("duration: %q %v", out, err)
	}
}
