package configsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/harness/health"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store/storetest"
)

func isolate(t *testing.T) {
	storetest.ClearStamp(t)
	previous := health.VersionDetector
	health.VersionDetector = func(context.Context, profile.Profile) health.VersionInfo {
		return health.VersionInfo{Version: "test", Installed: true}
	}
	t.Cleanup(func() { health.VersionDetector = previous })
	previousReady := health.HarnessReady
	health.HarnessReady = func(context.Context, string, string) (bool, error) { return true, nil }
	t.Cleanup(func() { health.HarnessReady = previousReady })

	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_NO_AUTO_UPDATE", "")
}

func seed(t *testing.T, ps ...profile.Profile) {
	t.Helper()
	s := &profile.Store{Path: profile.DefaultPath()}
	for _, p := range ps {
		if err := s.Upsert(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
}

func mock(name string) profile.Profile {
	return profile.Profile{Name: name, Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5}
}

func TestProfilesNeverReturnEnvValues(t *testing.T) {
	isolate(t)
	p := mock("opus")
	p.Default, p.Env = true, map[string]string{"API_KEY": "sk-secret-value", "REGION": "eu"}
	seed(t, p, mock("sol"))
	list, err := Profiles()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(list)
	if strings.Contains(string(b), "sk-secret-value") || strings.Contains(string(b), `"eu"`) {
		t.Fatalf("payload leaks env values: %s", b)
	}
	if list.Default != "opus" || strings.Join(list.Profiles[0].EnvKeys, ",") != "API_KEY,REGION" {
		t.Fatalf("list = %+v", list)
	}
}

func TestSaveProfileKeepsUnseenEnvAndRenames(t *testing.T) {
	isolate(t)
	p := mock("opus")
	p.Env = map[string]string{"API_KEY": "sk-1", "OLD": "x"}
	seed(t, p, mock("sol"))
	edit := ProfileEdit{Original: "opus", Profile: view(p), EnvSet: map[string]string{"NEW": "y"}, EnvRemove: []string{"OLD"}}
	edit.Profile.Name, edit.Profile.Model = "opus-2", "m2"
	v, err := (&Service{}).SaveProfile(t.Context(), edit)
	if err != nil {
		t.Fatal(err)
	}
	if v.Name != "opus-2" || strings.Join(v.EnvKeys, ",") != "API_KEY,NEW" {
		t.Fatalf("view = %+v", v)
	}
	s, _ := profile.Load(profile.DefaultPath())
	got, ok := s.Get("opus-2")
	if !ok || got.Env["API_KEY"] != "sk-1" || got.Env["NEW"] != "y" || got.Model != "m2" {
		t.Fatalf("stored = %+v", got)
	}
	if _, ok := s.Get("opus"); ok {
		t.Fatal("rename left the old profile")
	}
	if _, err := (&Service{}).SaveProfile(t.Context(), ProfileEdit{Original: "sol", Profile: view(mock("opus-2"))}); err == nil {
		t.Fatal("rename onto an existing name must fail")
	}
	bad := view(mock("x"))
	bad.Taste = 11
	if _, err := (&Service{}).SaveProfile(t.Context(), ProfileEdit{Profile: bad}); err == nil {
		t.Fatal("SaveProfile must validate like dyna profiles add")
	}
}

func TestDefaultDisableRemove(t *testing.T) {
	isolate(t)
	seed(t, mock("opus"), mock("sol"))
	if err := (&Service{}).SetDefaultProfile(t.Context(), "sol"); err != nil {
		t.Fatal(err)
	}
	if err := (&Service{}).SetProfileDisabled(t.Context(), "opus", true); err != nil {
		t.Fatal(err)
	}
	if err := (&Service{}).SetDefaultProfile(t.Context(), "opus"); err == nil {
		t.Fatal("a disabled profile must not become the default")
	}
	list, _ := Profiles()
	if list.Default != "sol" || !list.Profiles[0].Disabled {
		t.Fatalf("list = %+v", list)
	}
	if err := (&Service{}).RemoveProfile(t.Context(), "opus"); err != nil {
		t.Fatal(err)
	}
	if err := (&Service{}).RemoveProfile(t.Context(), "opus"); err == nil {
		t.Fatal("removing a missing profile must fail")
	}
}

func TestSettingsItemsAndSet(t *testing.T) {
	isolate(t)
	items, err := Items()
	if err != nil || len(items) != 37 {
		t.Fatalf("Items = %d, %v", len(items), err)
	}
	for _, it := range items {
		if it.Help == "" || it.Kind == "" {
			t.Fatalf("item %s lacks help or kind", it.Key)
		}
	}
	var retention *Item
	for i := range items {
		if items[i].Key == settings.KeyRunsRetention {
			retention = &items[i]
		}
	}
	if retention == nil || retention.Display != "14d" || retention.Source != settings.SourceDefault {
		t.Fatalf("run retention item = %+v", retention)
	}
	for _, value := range []string{"7d", "off"} {
		if _, err := Set(t.Context(), settings.KeyRunsRetention, value); err != nil {
			t.Fatal(err)
		}
	}
	it, err := Set(t.Context(), settings.KeySubagentMaxDepth, "4")
	if err != nil || it.Value != 4 || it.Source != settings.SourceFile {
		t.Fatalf("Set depth = %+v, %v", it, err)
	}
	if _, err := Set(t.Context(), settings.KeySubagentMaxDepth, "0"); err == nil {
		t.Fatal("depth 0 must fail")
	}
	// The CI disk floor is a byte count the page shows, so its item carries the
	// saved value, the file source and help that names the unit.
	floor, err := Set(t.Context(), settings.KeyCIDiskFloorGB, "40")
	if err != nil || floor.Value != 40 || floor.Source != settings.SourceFile || floor.Kind != "int" {
		t.Fatalf("Set disk floor = %+v, %v", floor, err)
	}
	if !strings.Contains(floor.Help, "GiB") {
		t.Fatalf("disk floor help = %q", floor.Help)
	}
	if _, err := Set(t.Context(), settings.KeyCIDiskFloorGB, "-1"); err == nil {
		t.Fatal("a negative disk floor must fail")
	}
	// The render node names the GPU computers start on and takes effect at
	// the next computer start.
	node, err := Set(t.Context(), settings.KeyComputersRenderNode, "/dev/dri/renderD129")
	if err != nil || node.Value != "/dev/dri/renderD129" || node.Source != settings.SourceFile || node.LiveVsRestart != settings.LiveNextOpen {
		t.Fatalf("Set render node = %+v, %v", node, err)
	}
	if _, err := Set(t.Context(), settings.KeyComputersRenderNode, "/dev/dri/card0"); err == nil {
		t.Fatal("a card path must fail")
	}
}

func TestCheckUpdateUsesCacheAndRespectsSwitches(t *testing.T) {
	isolate(t)
	dir := filepath.Join(os.Getenv("XDG_DATA_HOME"), "dyna")
	_ = os.MkdirAll(dir, 0o755)
	// A cache written before dyna.db, which the release check store imports once.
	state := fmt.Sprintf(`{"lastChecked":%q,"release":{"tag_name":"v9.9.9"}}`, time.Now().UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(filepath.Join(dir, "update-check.json"), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	n, err := CheckUpdate(ctx, "v0.4.1", false)
	if err != nil || n.Latest != "v9.9.9" || !strings.Contains(n.Text, "dyna update") {
		t.Fatalf("notice = %+v, %v", n, err)
	}
	if n, _ := CheckUpdate(ctx, "v0.4.1", true); n.Text != "" {
		t.Fatal("DYNA_NO_AUTO_UPDATE must silence the notice")
	}
	if n, _ := CheckUpdate(ctx, "dev", false); n.Text != "" {
		t.Fatal("a development build must not get a notice")
	}
	if _, err := Set(t.Context(), settings.KeyAutoCheck, "false"); err != nil {
		t.Fatal(err)
	}
	if n, _ := CheckUpdate(ctx, "v0.4.1", false); n.Text != "" {
		t.Fatal("updater.autoCheck=false must silence the notice")
	}
}

func TestSettingsIgnoreAndPreserveOldAdmissionPolicy(t *testing.T) {
	isolate(t)
	path := filepath.Join(os.Getenv("XDG_DATA_HOME"), "dyna", "admission-policy.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const old = "invalid legacy policy; leave this file alone"
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := Items()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Key == "admission.maxAgents" {
			t.Fatalf("removed setting: %+v", item)
		}
	}
	if _, err := Set(t.Context(), settings.KeySubagentMaxDepth, "4"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != old {
		t.Fatalf("legacy policy changed: %q, %v", data, err)
	}
}

func TestSettingDisplaysKeepRawValues(t *testing.T) {
	isolate(t)
	items, err := Items()
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		switch it.Key {
		case settings.KeyMaxCardBytes, settings.KeyMaxFileBytes:
			if it.Kind != "bytes" || it.Value != int64(25<<20) || it.Display != "25 MiB" {
				t.Fatalf("size item: %+v", it)
			}
			if !strings.Contains(it.Help, "Current post total: 200 MiB") || strings.Contains(it.Help, "209715200") {
				t.Fatalf("help: %s", it.Help)
			}
		case settings.KeySteeringToolWait:
			if it.Value != "2m0s" || it.Display != "2m" {
				t.Fatalf("duration item: %+v", it)
			}
		}
	}
	item, err := Set(t.Context(), settings.KeyMaxFileBytes, "512 KiB")
	if err != nil {
		t.Fatal(err)
	}
	if item.Value != int64(512<<10) || item.Display != "512 KiB" || !strings.Contains(item.Help, "Current post total: 28.5 MiB") {
		t.Fatalf("updated: %+v", item)
	}
}

func TestSaveProfileModelLists(t *testing.T) {
	isolate(t)
	p := mock("worker")
	p.Model = "legacy"
	seed(t, p)
	ed := ProfileEdit{Original: p.Name, Profile: view(p)}
	if strings.Join(ed.Profile.Models, ",") != "legacy" {
		t.Fatalf("legacy view: %+v", ed.Profile)
	}
	ed.Profile.Model = "replacement"
	v, err := (&Service{}).SaveProfile(t.Context(), ed)
	if err != nil {
		t.Fatal(err)
	}
	if v.Model != "replacement" || strings.Join(v.Models, ",") != "replacement" {
		t.Fatalf("scalar edit: %+v", v)
	}
	s, err := loadProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if s.Profiles[0].Models != nil {
		t.Fatalf("single model was stored as list: %+v", s.Profiles[0])
	}
	ed.Profile.Model = "ignored"
	ed.Profile.Models = []string{" first ", "", "second", "first"}
	v, err = (&Service{}).SaveProfile(t.Context(), ed)
	if err != nil {
		t.Fatal(err)
	}
	if v.Model != "first" || strings.Join(v.Models, ",") != "first,second" {
		t.Fatalf("chain view: %+v", v)
	}
	s, err = loadProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(s.Profiles[0].Models, ",") != "first,second" {
		t.Fatalf("stored chain: %+v", s.Profiles[0])
	}
	v.Models[0] = "changed"
	if s.Profiles[0].Models[0] != "first" {
		t.Fatal("view shares model storage")
	}
}

func TestPrivateRuntimeSelectionRoundTrips(t *testing.T) {
	isolate(t)
	p := mock("private")
	p.Harness = profile.HarnessPi
	p.RuntimeVersion = "1.2.3"
	seed(t, p)
	v := view(p)
	got, err := (&Service{}).SaveProfile(t.Context(), ProfileEdit{Original: p.Name, Profile: v})
	if err != nil {
		t.Fatal(err)
	}
	if got.RuntimeVersion != "1.2.3" {
		t.Fatal("runtime selection was lost")
	}
	s, err := profile.Load(profile.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := s.Get(p.Name)
	if stored.RuntimeVersion != "1.2.3" {
		t.Fatal("profile did not retain private runtime version")
	}
}

// A new profile needs a harness that Dyna has or can install now.
func TestSaveProfileRejectsHarnessDynaCannotInstall(t *testing.T) {
	isolate(t)
	health.HarnessReady = func(context.Context, string, string) (bool, error) {
		return false, errors.New("Install pi on the Harnesses page first.")
	}
	p := mock("grok")
	p.Harness = profile.HarnessPi
	_, err := (&Service{}).SaveProfile(t.Context(), ProfileEdit{Profile: view(p)})
	if err == nil || err.Error() != "Install pi on the Harnesses page first." {
		t.Fatalf("SaveProfile() = %v; want the install hint", err)
	}
	if _, err := (&Service{}).SaveProfile(t.Context(), ProfileEdit{Profile: view(mock("own"))}); err != nil {
		t.Fatalf("a mock profile needs no private copy: %v", err)
	}
}

// A pinned version reaches the readiness check, as first-use install uses it.
func TestSaveProfilePassesPinnedVersionToReadiness(t *testing.T) {
	isolate(t)
	var got string
	health.HarnessReady = func(_ context.Context, _, version string) (bool, error) { got = version; return false, nil }
	p := mock("pinned")
	p.Harness, p.RuntimeVersion = profile.HarnessPi, "2026.10.17-abcdef0"
	if _, err := (&Service{}).SaveProfile(t.Context(), ProfileEdit{Profile: view(p)}); err != nil {
		t.Fatal(err)
	}
	if got != "2026.10.17-abcdef0" {
		t.Fatalf("readiness checked version %q, want the pinned version", got)
	}
}

// The profile list tells the wizard which harnesses a new profile cannot use.
func TestProfilesListsUnreadyHarnesses(t *testing.T) {
	isolate(t)
	health.HarnessReady = func(_ context.Context, h, _ string) (bool, error) {
		if h == profile.HarnessPi {
			return false, errors.New("Install pi on the Harnesses page first.")
		}
		return true, nil
	}
	list, err := Profiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Unready) != 1 || list.Unready[profile.HarnessPi] != "Install pi on the Harnesses page first." {
		t.Fatalf("Unready = %v; want only pi with its fix", list.Unready)
	}
}

func TestSaveProfileKeepsCompact(t *testing.T) {
	isolate(t)
	p := mock("cheap")
	p.Harness = profile.HarnessClaudeCode
	seed(t, p)
	ed := ProfileEdit{Original: p.Name, Profile: view(p)}
	ed.Profile.Compact = &profile.Compact{Tokens: 95000}
	if v, err := (&Service{}).SaveProfile(t.Context(), ed); err != nil || v.Compact == nil || v.Compact.Tokens != 95000 {
		t.Fatalf("view = %+v, %v", v.Compact, err)
	}
	s, _ := profile.Load(profile.DefaultPath())
	if got, _ := s.Get("cheap"); got.Compact == nil || got.Compact.Tokens != 95000 {
		t.Fatalf("stored = %+v", got.Compact)
	}
	ed.Profile.Compact = nil
	if v, err := (&Service{}).SaveProfile(t.Context(), ed); err != nil || v.Compact != nil {
		t.Fatalf("clearing kept %+v, %v", v.Compact, err)
	}
	ed.Profile.Harness, ed.Profile.Compact = profile.HarnessMock, &profile.Compact{Tokens: 95000}
	if _, err := (&Service{}).SaveProfile(t.Context(), ed); err == nil || !strings.Contains(err.Error(), "no compaction threshold setting") {
		t.Fatalf("SaveProfile() = %v; want the unsupported-compaction error", err)
	}
}

// The Settings editor saves any set of uses, and the list reads it back.
func TestSaveProfileDiagnosticsRoundTrip(t *testing.T) {
	isolate(t)
	seed(t, mock("sol"))
	edit := ProfileEdit{Original: "sol", Profile: view(mock("sol"))}
	edit.Profile.Diagnostics = 9
	v, err := (&Service{}).SaveProfile(t.Context(), edit)
	if err != nil || v.Diagnostics != 9 {
		t.Fatalf("save = %+v, %v", v, err)
	}
	list, err := Profiles()
	if err != nil || list.Profiles[0].Diagnostics != 9 {
		t.Fatalf("list after save = %+v, %v", list.Profiles, err)
	}
}

func TestSaveProfileUsesRoundTrip(t *testing.T) {
	isolate(t)
	seed(t, mock("sol"))
	for _, uses := range [][]string{{profile.UseServices}, {profile.UseAgents}, {profile.UseWorkflows, profile.UseServices}, profile.AllUses} {
		edit := ProfileEdit{Original: "sol", Profile: view(mock("sol"))}
		edit.Profile.Uses = uses
		v, err := (&Service{}).SaveProfile(t.Context(), edit)
		if err != nil || !slices.Equal(v.Uses, uses) {
			t.Fatalf("save %v = %+v, %v", uses, v, err)
		}
		list, err := Profiles()
		if err != nil || !slices.Equal(list.Profiles[0].Uses, uses) {
			t.Fatalf("list after %v = %+v, %v", uses, list.Profiles, err)
		}
	}
	// Unchecking every use is refused, not read as all three.
	edit := ProfileEdit{Original: "sol", Profile: view(mock("sol"))}
	edit.Profile.Uses = []string{}
	if _, err := (&Service{}).SaveProfile(t.Context(), edit); err == nil || !strings.Contains(err.Error(), "at least one use") {
		t.Fatalf("no uses err = %v", err)
	}
}

func TestSetDoctorConditionChecksTheGrammar(t *testing.T) {
	isolate(t)
	if _, err := Set(t.Context(), settings.KeyDoctorCondition, "load>2/core for 5m or memory>85%"); err != nil {
		t.Fatalf("valid condition: %v", err)
	}
	if _, err := Set(t.Context(), settings.KeyDoctorCondition, "banana"); err == nil {
		t.Fatal("an invalid condition was saved")
	}
}
