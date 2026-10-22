package configsvc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	harnesshealth "dyna-agent/internal/harness/health"
	"dyna-agent/internal/profile"
)

func TestProfileAPIReportsObservedHealthAndVersionDrift(t *testing.T) {
	isolate(t)
	p := mock("health")
	p.DeclareOutput()
	p.Uses = []string{profile.UseAgents}
	seed(t, p)
	store := harnesshealth.New("")
	store.RecordTurn(p, "v1", harnesshealth.Observation{
		At: time.Unix(100, 0), Successful: true, StdoutSeen: true, RecognizedRecords: 2,
		TextSeen: true, ToolSeen: true, PositiveUsageSeen: true,
	})
	harnesshealth.VersionDetector = func(context.Context, profile.Profile) harnesshealth.VersionInfo {
		return harnesshealth.VersionInfo{Version: "v2", Installed: true}
	}
	result, err := CheckProfile(context.Background(), p.Name, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Support.Status != harnesshealth.UnverifiedSinceUpdate || result.Support.Version != "v2" || result.Support.Observed == nil || !result.Support.Observed.ToolSeen {
		t.Fatalf("support health = %#v", result.Support)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"declared":`, `"observed":`, `"version":"v2"`, `"lastCheckedAt":`, `"recognizedOutput":true`, `"installed":true`} {
		if !strings.Contains(string(data), field) {
			t.Fatalf("API lacks %s: %s", field, data)
		}
	}
	o := harnesshealth.Observation{Successful: true, StdoutSeen: true}
	store.RecordTurn(p, "v2", o)
	result, err = CheckProfile(context.Background(), p.Name, false)
	if err != nil || result.Support.Status != harnesshealth.OutputNotUnderstood {
		t.Fatalf("unreadable output = %#v, %v", result, err)
	}
}

func TestCheckProfileRejectsUnknownAndDisabled(t *testing.T) {
	isolate(t)
	p := mock("disabled")
	p.Disabled = true
	seed(t, p)
	for _, name := range []string{"absent", "disabled"} {
		if _, err := CheckProfile(context.Background(), name, false); err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
}

func TestChangingHarnessUsesNewNativeDialect(t *testing.T) {
	isolate(t)
	p := mock("switch")
	p.Harness = profile.HarnessCodex
	p.Output = p.EffectiveOutput()
	seed(t, p)
	v := view(p)
	v.Harness = profile.HarnessClaudeCode
	saved, err := (&Service{}).SaveProfile(t.Context(), ProfileEdit{Original: p.Name, Profile: v})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Support.Dialect != "claude-stream-json" {
		t.Fatalf("support retained old dialect: %#v", saved.Support)
	}
	store, err := loadProfiles()
	if err != nil {
		t.Fatal(err)
	}
	current, _ := store.Get(p.Name)
	if current.Output == nil || current.Output.Dialect != "claude-stream-json" {
		t.Fatal("harness edit did not persist the new dialect")
	}
}
