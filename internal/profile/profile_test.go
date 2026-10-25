package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"dyna-agent/internal/workeroutput"
)

func TestOutputDeclarationCompatibilityAndValidation(t *testing.T) {
	legacy := Profile{Name: "legacy", Harness: HarnessMock, Taste: 5, Intelligence: 5, Cost: 5}
	b, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"output"`) {
		t.Fatalf("legacy profile gained an output declaration: %s", b)
	}

	legacy.Harness = HarnessPi
	legacy.Output = &workeroutput.Declaration{Dialect: workeroutput.DialectPiJSON}
	b, err = json.Marshal(legacy)
	if err != nil || !strings.Contains(string(b), `"dialect":"pi-json"`) {
		t.Fatalf("output declaration did not serialize: %s, %v", b, err)
	}
	if err := legacy.Validate(); err != nil {
		t.Fatal(err)
	}
	legacy.Output.Dialect = "arbitrary-code"
	if err := legacy.Validate(); err == nil || !strings.Contains(err.Error(), "unknown output dialect") {
		t.Fatalf("invalid output declaration error = %v", err)
	}
}

func TestUseSerializationAndLegacyFieldDropped(t *testing.T) {
	legacy := []byte(`{"version":2,"profiles":[{"name":"legacy","description":"","harness":"mock","taste":5,"intelligence":5,"cost":5,"disableSubagents":true}]}`)
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if store.Profiles[0].Uses != nil {
		t.Fatal("legacy profile unexpectedly became subagents-only")
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "disableSubagents") {
		t.Fatalf("saved store retained the removed disableSubagents field: %s", raw)
	}

	store.Profiles[0].Uses = []string{UseAgents}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(reloaded.Profiles[0].Uses, []string{UseAgents}) {
		t.Fatal("use did not round-trip")
	}
	b, err := json.Marshal(Profile{Name: "default-false"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "subagentOnly") || strings.Contains(string(b), "disableSubagents") {
		t.Fatalf("false subagentOnly (or removed field) was serialized: %s", b)
	}
	b, err = json.Marshal(Profile{Name: "solo", Uses: []string{UseAgents}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"uses":["agents"]`) {
		t.Fatalf("uses did not serialize under its JSON name: %s", b)
	}
}

func TestWorkflowDefaultSkipsSubagentsOnly(t *testing.T) {
	store := Store{Profiles: []Profile{
		{Name: "solo", Default: true, Uses: []string{UseAgents}},
		{Name: "worker"},
	}}
	// The configured default still serves subagent sessions.
	p, ok := store.DefaultProfile()
	if !ok || p.Name != "solo" || !slices.Equal(p.Uses, []string{UseAgents}) {
		t.Fatalf("DefaultProfile() = %#v, %v", p, ok)
	}
	p, ok = store.SubagentDefaultProfile()
	if !ok || p.Name != "solo" {
		t.Fatalf("SubagentDefaultProfile() = %#v, %v", p, ok)
	}
	// Workflow default selection skips the marked default.
	p, ok = store.WorkflowDefaultProfile()
	if !ok || p.Name != "worker" {
		t.Fatalf("WorkflowDefaultProfile() = %#v, %v, want worker", p, ok)
	}

	// The fallback (first enabled) also skips subagent-only profiles.
	store = Store{Profiles: []Profile{
		{Name: "aaa-solo", Uses: []string{UseAgents}},
		{Name: "worker"},
	}}
	p, ok = store.WorkflowDefaultProfile()
	if !ok || p.Name != "worker" {
		t.Fatalf("WorkflowDefaultProfile() fallback = %#v, %v, want worker", p, ok)
	}

	// With no workflow-eligible profile there is no workflow default, but a
	// subagent default still exists.
	store = Store{Profiles: []Profile{{Name: "solo", Uses: []string{UseAgents}}}}
	if p, ok := store.WorkflowDefaultProfile(); ok {
		t.Fatalf("WorkflowDefaultProfile() = %#v, want none", p)
	}
	if p, ok := store.DefaultProfile(); !ok || p.Name != "solo" {
		t.Fatalf("DefaultProfile() = %#v, %v, want solo", p, ok)
	}

	solo := Profile{Name: "solo", Uses: []string{UseAgents}}
	if solo.WorkflowEligible() {
		t.Fatal("subagent-only profile reported workflow-eligible")
	}
	worker := Profile{Name: "worker"}
	if !worker.WorkflowEligible() {
		t.Fatal("plain profile reported workflow-ineligible")
	}
	disabled := Profile{Name: "off", Disabled: true}
	if disabled.WorkflowEligible() {
		t.Fatal("disabled profile reported workflow-eligible")
	}
}

func TestLoadRejectsInvalidProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	contents := `{"version":2,"profiles":[{"name":"","harness":"pi","taste":5,"intelligence":5,"cost":5}]}`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), `invalid profile ""`) || !strings.Contains(err.Error(), "profile name is required") {
		t.Fatalf("Load() error = %v, want an invalid profile", err)
	}
}

func TestDiagnosticsDefaultsToNeutral(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	contents := `{"version":2,"profiles":[{"name":"old","harness":"mock","taste":5,"intelligence":5,"cost":5}]}`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatalf("Load() of a profile without the new field: %v", err)
	}
	p, _ := store.Get("old")
	if p.Diagnostics != 0 || p.EffectiveDiagnostics() != 5 {
		t.Fatalf("unset diagnostics = %d effective %d, want 0 effective 5", p.Diagnostics, p.EffectiveDiagnostics())
	}
	// Zero stays legal so old profiles load, but a rating outside 1..10 is refused.
	for _, bad := range []Profile{
		{Name: "x", Harness: HarnessMock, Taste: 5, Intelligence: 5, Cost: 5, Diagnostics: 11},
		{Name: "x", Harness: HarnessMock, Taste: 5, Intelligence: 5, Cost: 5, Diagnostics: -1},
	} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("Validate(%+v) accepted an out-of-range diagnostics rating", bad)
		}
	}
	if err := store.Upsert(Profile{Name: "rated", Harness: HarnessMock, Taste: 5, Intelligence: 5, Cost: 5, Diagnostics: 9}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if rated, _ := reloaded.Get("rated"); rated.Diagnostics != 9 {
		t.Fatalf("round trip diagnostics = %d, want 9", rated.Diagnostics)
	}
}

func TestLoadIgnoresLegacyAdmissionCost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	legacy := `{"version":2,"profiles":[{"name":"legacy","harness":"mock","taste":5,"intelligence":5,"cost":5,"admissionCost":"heavy"}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Profiles) != 1 || store.Profiles[0].Name != "legacy" {
		t.Fatalf("loaded profiles = %#v", store.Profiles)
	}
	encoded, err := json.Marshal(store.Profiles[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "admissionCost") || strings.Contains(string(encoded), "heavy") {
		t.Fatalf("loaded profile retained admission cost: %s", encoded)
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "admissionCost") || strings.Contains(string(raw), `"heavy"`) {
		t.Fatalf("saved store retained admission cost: %s", raw)
	}
}

func TestValidateRejectsNegativeExecutionLimits(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*Profile)
	}{
		{name: "timeout", set: func(p *Profile) { p.TimeoutSec = -1 }},
		{name: "concurrency", set: func(p *Profile) { p.MaxConcurrent = -1 }},
		{name: "call cap", set: func(p *Profile) { p.MaxCallsPerRun = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Profile{Name: "valid", Harness: HarnessMock, Taste: 5, Intelligence: 5, Cost: 5}
			tc.set(&p)
			if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "must be non-negative") {
				t.Fatalf("Validate() error = %v, want negative-limit rejection", err)
			}
		})
	}
}

func TestNormalizeModels(t *testing.T) {
	for _, tt := range []struct {
		name, model string
		models      []string
		wantModel   string
		wantModels  []string
	}{
		{"legacy", " primary ", nil, "primary", nil},
		{"empty", " ", []string{"", " "}, "", nil},
		{"single", "", []string{" first ", "first", " "}, "first", []string{"first"}},
		{"override", " replacement ", []string{"first", " first "}, "replacement", []string{"replacement"}},
		{"chain", "ignored", []string{" first ", "", "second", "first", " third "}, "first", []string{"first", "second", "third"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			model, models := NormalizeModels(tt.model, tt.models)
			if model != tt.wantModel || !reflect.DeepEqual(models, tt.wantModels) {
				t.Fatalf("NormalizeModels = %q, %#v; want %q, %#v", model, models, tt.wantModel, tt.wantModels)
			}
			p := Profile{Model: tt.model, Models: tt.models}
			p.NormalizeModels()
			if p.Model != model || !reflect.DeepEqual(p.Models, models) {
				t.Fatalf("method and function differ: %+v", p)
			}
			effective := p.EffectiveModels()
			if len(effective) > 0 {
				effective[0] = "changed"
				if p.Model == "changed" || len(p.Models) > 0 && p.Models[0] == "changed" {
					t.Fatal("effective models share profile storage")
				}
			}
		})
	}
}

func TestLoadRetainsNormalizedModels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	s := &Store{Path: path, Version: storeVersion, Profiles: []Profile{{Name: "chain", Harness: HarnessMock, Model: "old", Models: []string{" first ", "", "second", "first"}, Taste: 5, Intelligence: 5, Cost: 5}}}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := loaded.Get("chain")
	if p.Model != "first" || !reflect.DeepEqual(p.Models, []string{"first", "second"}) {
		t.Fatalf("loaded profile was not normalized: %+v", p)
	}
}

func TestModelListsAreCloned(t *testing.T) {
	original := Profile{Models: []string{"first", "second"}}
	cloned := cloneProfile(original)
	cloned.Models[0] = "clone"
	if !reflect.DeepEqual(original.Models, []string{"first", "second"}) {
		t.Fatalf("model list shares storage: %v", original.Models)
	}
}

func TestStoredSystemRuntimeLoadsAsDynaManaged(t *testing.T) {
	var p Profile
	if err := json.Unmarshal([]byte(`{"name":"old","harness":"pi","runtime":"system","taste":5,"intelligence":5,"cost":5}`), &p); err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(); err != nil || !p.DynaManaged() {
		t.Fatalf("Validate() = %v, DynaManaged() = %v; want a valid Dyna-managed profile", err, p.DynaManaged())
	}
	plain := Profile{Name: "own", Harness: HarnessMock, RuntimeVersion: "1.0.0", Taste: 5, Intelligence: 5, Cost: 5}
	if plain.DynaManaged() || plain.Validate() == nil {
		t.Fatal("an unmanaged harness has no Dyna-managed copy to pin")
	}
}

func TestLoadRejectsUnsupportedHarness(t *testing.T) {
	for _, harness := range []string{"cursor-agent", "unknown"} {
		t.Run(harness, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "profiles.json")
			raw := `{"version":2,"profiles":[{"name":"unsupported","harness":"` + harness + `","taste":5,"intelligence":5,"cost":5},{"name":"valid","harness":"mock","taste":5,"intelligence":5,"cost":5}]}`
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "invalid profile") || !strings.Contains(err.Error(), harness) {
				t.Fatalf("Load() error = %v, want invalid harness %q", err, harness)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != raw {
				t.Fatal("Load changed the invalid store")
			}
		})
	}
}

// Each use is its own switch; no uses means all three.
func TestProfileUsesMatrix(t *testing.T) {
	check := func(p Profile, workflows, agents, services bool) {
		t.Helper()
		if p.WorkflowEligible() != workflows || p.ForSubagents() != agents || p.ForServices() != services {
			t.Errorf("%v: workflows=%v agents=%v services=%v", p.Uses, p.WorkflowEligible(), p.ForSubagents(), p.ForServices())
		}
	}
	check(Profile{}, true, true, true)
	check(Profile{Uses: []string{UseWorkflows}}, true, false, false)
	check(Profile{Uses: []string{UseWorkflows, UseServices}}, true, false, true)
	check(Profile{Uses: []string{UseAgents}}, false, true, false)
	check(Profile{Disabled: true}, false, false, false)
	// Validate orders and dedupes the list, folds all three to empty, and refuses unknown uses.
	p := Profile{Name: "x", Harness: HarnessMock, Taste: 5, Intelligence: 5, Cost: 5, Uses: []string{UseServices, UseWorkflows, UseServices}}
	if err := p.Validate(); err != nil || !slices.Equal(p.Uses, []string{UseWorkflows, UseServices}) {
		t.Fatalf("Validate = %v, uses %v", err, p.Uses)
	}
	p.Uses = []string{UseServices, UseAgents, UseWorkflows}
	if err := p.Validate(); err != nil || p.Uses != nil {
		t.Fatalf("all three = %v, %v", p.Uses, err)
	}
	p.Uses = []string{"all"}
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "workflows, agents, services") {
		t.Fatalf("unknown use err = %v", err)
	}
}

// Each kind of caller skips profiles it may not use, even a marked default.
func TestDefaultsFollowProfileUse(t *testing.T) {
	store := Store{Profiles: []Profile{
		{Name: "svc", Default: true, Uses: []string{UseServices}},
		{Name: "sub", Uses: []string{UseAgents}},
		{Name: "any"},
	}}
	for _, tc := range []struct {
		name string
		pick func() (*Profile, bool)
		want string
	}{
		{"subagent", store.SubagentDefaultProfile, "sub"},
		{"workflow", store.WorkflowDefaultProfile, "any"},
		{"service", store.ServiceDefaultProfile, "svc"},
	} {
		if p, ok := tc.pick(); !ok || p.Name != tc.want {
			t.Errorf("%s default = %v, %v; want %s", tc.name, p, ok, tc.want)
		}
	}
}

// A store from a Dyna that still had a retired harness is refused, not
// rewritten: Load names the file, the profile, the retired harness, the
// harnesses Dyna runs, and how to fix the file. No profile is dropped for the
// owner, so the mistake stays visible.
func TestLoadRefusesAProfileOfARetiredHarness(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	contents := `{"version":2,"profiles":[
		{"name":"old-custom","harness":"custom","command":["/bin/true"],"taste":5,"intelligence":5,"cost":5},
		{"name":"kept","harness":"pi","taste":5,"intelligence":5,"cost":5}]}`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() accepted a store that names a retired harness")
	}
	for _, want := range []string{path, `"old-custom"`, `unknown harness "custom"`, "harnesses", "remove the profile"} {
		if want == "harnesses" {
			if !strings.Contains(err.Error(), HarnessPi) || !strings.Contains(err.Error(), HarnessMock) {
				t.Errorf("Load() error does not name the harnesses Dyna runs: %v", err)
			}
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Load() error lacks %q: %v", want, err)
		}
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != contents {
		t.Fatalf("Load() rewrote the store:\n%s", after)
	}
}
