package profiles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"dyna-agent/internal/harness/health"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/workeroutput"
)

func TestProfilesAddUseFlagAndJSON(t *testing.T) {
	// The removed flag must not come back as a compatibility alias.
	cmd := NewCommand()
	for _, sub := range cmd.Commands() {
		if sub.Name() == "add" {
			if flag := sub.Flags().Lookup("disable-subagents"); flag != nil {
				t.Fatal("profiles add still exposes the removed --disable-subagents flag")
			}
			if flag := sub.Flags().Lookup("use"); flag == nil {
				t.Fatal("profiles add does not expose --use")
			}
		}
	}

	isolateProfiles(t)
	uses := func() []string {
		t.Helper()
		store, err := profile.Load(profile.DefaultPath())
		if err != nil {
			t.Fatal(err)
		}
		p, ok := store.Get("solo")
		if !ok {
			t.Fatal("profile solo is missing")
		}
		return p.Uses
	}
	runProfilesCommand(t, "add", "--name", "solo", "--harness", "mock", "--use", "agents")
	if got := uses(); !slices.Equal(got, []string{profile.UseAgents}) {
		t.Fatalf("--use agents saved %v", got)
	}
	list := runProfilesCommand(t, "list", "--json")
	show := runProfilesCommand(t, "show", "solo")
	if !strings.Contains(list, `"uses": [`) || !strings.Contains(show, `"agents"`) {
		t.Fatalf("JSON output omitted uses: list=%s show=%s", list, show)
	}
	if strings.Contains(list, "disableSubagents") || strings.Contains(show, "disableSubagents") {
		t.Fatalf("JSON output retained the removed disableSubagents field: list=%s show=%s", list, show)
	}
	if human := runProfilesCommand(t, "list"); !strings.Contains(human, "solo  (agents only)") {
		t.Fatalf("human list output does not show the uses: %s", human)
	}
	// Any subset, in any order, saves in display order; all three clears the limit.
	runProfilesCommand(t, "set", "solo", "--use", "services,workflows")
	if got := uses(); !slices.Equal(got, []string{profile.UseWorkflows, profile.UseServices}) {
		t.Fatalf("--use services,workflows saved %v", got)
	}
	runProfilesCommand(t, "set", "solo", "--use", "workflows,agents,services")
	if got := uses(); got != nil {
		t.Fatalf("all three uses saved %v, want none", got)
	}
}

func TestProfilesAddOutputDialectFlag(t *testing.T) {
	isolateProfiles(t)
	runProfilesCommand(t, "add", "--name", "direct-codex", "--harness", "custom", "--command", "fixture", "--output-dialect", workeroutput.DialectCodexJSONL)
	store, err := profile.Load(profile.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	p, ok := store.Get("direct-codex")
	if !ok || p.Output == nil || p.Output.Dialect != workeroutput.DialectCodexJSONL {
		t.Fatalf("saved output declaration = %#v, %v", p, ok)
	}

	cmd := NewCommand()
	cmd.SetArgs([]string{"add", "--name", "bad", "--harness", "mock", "--output-dialect", "runtime-plugin"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unknown output dialect") {
		t.Fatalf("invalid output dialect error = %v", err)
	}
}

func TestProfilesListOmitsAdmissionCost(t *testing.T) {
	isolateProfiles(t)
	runProfilesCommand(t, "add", "--name", "worker", "--harness", "mock")
	list := runProfilesCommand(t, "list")
	jsonList := runProfilesCommand(t, "list", "--json")
	if strings.Contains(list, "admission") || strings.Contains(jsonList, "admissionCost") {
		t.Fatalf("profile list retained admission cost: human=%s json=%s", list, jsonList)
	}
}

func TestProfilesAddRejectsManagedFlag(t *testing.T) {
	add, _, err := NewCommand().Find([]string{"add"})
	if err != nil {
		t.Fatal(err)
	}
	if err := add.ParseFlags([]string{"--name", "custom", "--harness", "mock", "--managed"}); err == nil || !strings.Contains(err.Error(), "unknown flag: --managed") {
		t.Fatalf("retired managed flag error = %v", err)
	}
}

func TestBundledProfilesPolicy(t *testing.T) {
	var bundle struct {
		Profiles []profile.Profile `json:"profiles"`
	}
	if err := json.Unmarshal(defaultProfilesJSON, &bundle); err != nil {
		t.Fatalf("parse bundled profiles: %v", err)
	}
	if len(bundle.Profiles) != 12 {
		t.Fatalf("bundled profile count = %d, want 12", len(bundle.Profiles))
	}

	efforts := map[string]string{"luna": "max", "terra": "high", "sol": "high", "sol-max": "xhigh", "astra": "xhigh"}
	dialects := map[string]string{
		"luna": workeroutput.DialectCodexJSONL, "sol": workeroutput.DialectCodexJSONL,
		"sol-max": workeroutput.DialectCodexJSONL, "terra": workeroutput.DialectCodexJSONL,
		"astra": workeroutput.DialectCodexJSONL,
		"agy":   workeroutput.DialectAcpJSON,
		"opus":  workeroutput.DialectClaudeStreamJSON, "fable": workeroutput.DialectClaudeStreamJSON,
		"sonnet": workeroutput.DialectClaudeStreamJSON, "haiku": workeroutput.DialectClaudeStreamJSON,
		"wonder": workeroutput.DialectMuseJSONL, "wonder-full": workeroutput.DialectMuseJSONL,
	}
	seen := make(map[string]bool)
	var raw struct {
		Profiles []map[string]any `json:"profiles"`
	}
	if err := json.Unmarshal(defaultProfilesJSON, &raw); err != nil {
		t.Fatal(err)
	}
	rawByName := make(map[string]map[string]any)
	for _, p := range raw.Profiles {
		rawByName[p["name"].(string)] = p
	}
	for _, p := range bundle.Profiles {
		seen[p.Name] = true
		if p.Description != "" {
			t.Errorf("%s description = %q, want empty", p.Name, p.Description)
		}
		if p.Managed {
			t.Errorf("%s is managed", p.Name)
		}
		// The removed field must not ship in the bundled defaults, and no
		// bundled profile is subagent-only: all stay workflow-eligible.
		if p.Uses != nil {
			t.Errorf("%s uses = %v, want all", p.Name, p.Uses)
		}
		if _, hasKey := rawByName[p.Name]["disableSubagents"]; hasKey {
			t.Errorf("%s ships the removed disableSubagents key", p.Name)
		}
		for _, key := range []string{"use", "uses"} {
			if _, hasKey := rawByName[p.Name][key]; hasKey {
				t.Errorf("%s ships a %s key", p.Name, key)
			}
		}
		if _, hasKey := rawByName[p.Name]["description"]; hasKey {
			t.Errorf("%s ships a description key", p.Name)
		}
		if _, hasKey := rawByName[p.Name]["managed"]; hasKey {
			t.Errorf("%s ships a managed key", p.Name)
		}
		for _, leftover := range []string{"admissionCost", "costClass", "weight"} {
			if _, hasKey := rawByName[p.Name][leftover]; hasKey {
				t.Errorf("%s ships leftover %s", p.Name, leftover)
			}
		}
		if want, supported := dialects[p.Name]; supported {
			if p.Output == nil || p.Output.Dialect != want {
				t.Errorf("%s output = %#v, want dialect %q", p.Name, p.Output, want)
			}
			if _, hasKey := rawByName[p.Name]["output"]; !hasKey {
				t.Errorf("%s is missing the output declaration", p.Name)
			}
		} else if p.Output != nil {
			t.Errorf("%s output = %#v, want deliberate raw fallback", p.Name, p.Output)
		} else if _, hasKey := rawByName[p.Name]["output"]; hasKey {
			t.Errorf("%s ships an output declaration without a verified fixture", p.Name)
		}
		if (p.Name == "opus" || p.Name == "fable") && p.Harness != profile.HarnessClaudeCode {
			t.Errorf("%s harness = %q, want claude-code", p.Name, p.Harness)
		}
		if want, ok := efforts[p.Name]; ok {
			got := ""
			for _, arg := range p.ExtraArgs {
				if strings.HasPrefix(arg, "model_reasoning_effort=") {
					got = strings.TrimPrefix(arg, "model_reasoning_effort=")
				}
			}
			if got != want {
				t.Errorf("%s reasoning effort = %q, want %q", p.Name, got, want)
			}
		}
	}
	for _, name := range []string{"agy", "astra", "fable", "haiku", "luna", "opus", "sol", "sol-max", "sonnet", "terra", "wonder", "wonder-full"} {
		if !seen[name] {
			t.Errorf("missing bundled profile %s", name)
		}
	}
	if agy := rawByName["agy"]; agy["harness"] != profile.HarnessAntigravity || agy["model"] != profile.AntigravityModel || agy["taste"] != float64(6) || agy["intelligence"] != float64(7) || agy["cost"] != float64(9) {
		t.Errorf("agy bundle = %#v, want Antigravity Gemini 3.8 Flash High with 6/7/9 stats", agy)
	} else if _, ok := agy["extraArgs"]; ok {
		t.Errorf("agy extraArgs = %#v, want none: Antigravity ACP refuses CLI extraArgs", agy["extraArgs"])
	}
	if wonder := rawByName["wonder"]; wonder["harness"] != profile.HarnessMuse || wonder["model"] != "muse-wonder-1.0" || wonder["taste"] != float64(9) || wonder["intelligence"] != float64(9) || wonder["cost"] != float64(10) {
		t.Errorf("wonder bundle = %#v, want Muse muse-wonder-1.0 with 9/9/10 stats (cost matches luna)", wonder)
	} else if args, ok := wonder["extraArgs"].([]any); !ok || !reflect.DeepEqual(args, []any{"--reasoning-effort", "high"}) {
		t.Errorf("wonder extraArgs = %#v, want --reasoning-effort high", wonder["extraArgs"])
	}
	if wonderFull := rawByName["wonder-full"]; wonderFull["harness"] != profile.HarnessMuse || wonderFull["model"] != "muse-wonder-full-1.0" || wonderFull["taste"] != float64(10) || wonderFull["intelligence"] != float64(10) || wonderFull["cost"] != float64(6) {
		t.Errorf("wonder-full bundle = %#v, want Muse muse-wonder-full-1.0 with 10/10/6 stats (cost matches sonnet)", wonderFull)
	} else if args, ok := wonderFull["extraArgs"].([]any); !ok || !reflect.DeepEqual(args, []any{"--reasoning-effort", "high"}) {
		t.Errorf("wonder-full extraArgs = %#v, want --reasoning-effort high", wonderFull["extraArgs"])
	}
	if astra := rawByName["astra"]; astra["model"] != "gpt-6-astra" || astra["taste"] != float64(8) || astra["intelligence"] != float64(10) || astra["cost"] != float64(3) || astra["maxConcurrent"] != float64(4) || astra["maxCallsPerRun"] != float64(8) {
		t.Errorf("astra bundle = %#v, want GPT-6 Astra with 8/10/3 stats and 4 concurrent / 8 per run", astra)
	}
	if opus := rawByName["opus"]; opus["harness"] != profile.HarnessClaudeCode || opus["model"] != "opus" || opus["taste"] != float64(10) || opus["intelligence"] != float64(10) || opus["cost"] != float64(4) || opus["maxConcurrent"] != float64(4) || opus["maxCallsPerRun"] != float64(8) {
		t.Errorf("opus bundle = %#v, want latest Claude Opus alias with 10/10/4 stats and 4 concurrent / 8 per run", opus)
	}
	if sonnet := rawByName["sonnet"]; sonnet["harness"] != profile.HarnessClaudeCode || sonnet["model"] != "sonnet" || sonnet["taste"] != float64(8) || sonnet["intelligence"] != float64(9) || sonnet["cost"] != float64(7) {
		t.Errorf("sonnet bundle = %#v, want latest Claude Sonnet alias with 8/9/7 stats", sonnet)
	}
	if haiku := rawByName["haiku"]; haiku["harness"] != profile.HarnessClaudeCode || haiku["model"] != "haiku" || haiku["taste"] != float64(7) || haiku["intelligence"] != float64(7) || haiku["cost"] != float64(6) {
		t.Errorf("haiku bundle = %#v, want latest Claude Haiku alias with 7/7/6 stats", haiku)
	}
	if fable := rawByName["fable"]; fable["model"] != "fable" || fable["taste"] != float64(10) || fable["intelligence"] != float64(9) || fable["cost"] != float64(2) || fable["maxCallsPerRun"] != float64(2) {
		t.Errorf("fable bundle = %#v, want Claude Fable with 10/9/2 stats and 2 calls per run", fable)
	}
	if _, hasKey := rawByName["fable"]["maxConcurrent"]; hasKey {
		t.Errorf("fable ships a maxConcurrent limit; only the per-run call cap is intended")
	}
	if luna := rawByName["luna"]; luna["intelligence"] != float64(8) {
		t.Errorf("luna bundle = %#v, want intelligence 8", luna)
	}
	var defaultProfile *profile.Profile
	for i := range bundle.Profiles {
		if bundle.Profiles[i].Default && !bundle.Profiles[i].Disabled {
			defaultProfile = &bundle.Profiles[i]
		}
	}
	if defaultProfile == nil || defaultProfile.Name != "luna" || defaultProfile.Harness != profile.HarnessCodex {
		t.Errorf("bundled default = %#v, want Codex luna", defaultProfile)
	}
	for _, name := range []string{"opus", "fable"} {
		p, ok := rawByName[name]
		if !ok || p["harness"] != profile.HarnessClaudeCode || p["default"] == true {
			t.Errorf("Claude Code profile %s was accidentally selected: %#v", name, p)
		}
	}
}

func TestProfilesInitCommandsAreSeparateAndNeverOverwrite(t *testing.T) {
	t.Run("codex", func(t *testing.T) {
		isolateProfiles(t)
		wantSol := profile.Profile{
			Name: "sol", Description: "mine", Harness: profile.HarnessMock,
			Taste: 3, Intelligence: 4, Cost: 5, Managed: true,
		}
		store := &profile.Store{Path: profile.DefaultPath(), Profiles: []profile.Profile{wantSol}}
		if err := store.Save(); err != nil {
			t.Fatal(err)
		}

		runProfilesCommand(t, "init-codex")
		got, err := profile.Load(profile.DefaultPath())
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Profiles) != 5 {
			t.Fatalf("Codex profile count = %d, want 5", len(got.Profiles))
		}
		for _, name := range []string{"opus", "fable"} {
			if _, ok := got.Get(name); ok {
				t.Fatalf("init-codex installed the Claude profile %s", name)
			}
		}
		if sol, ok := got.Get("sol"); !ok || !reflect.DeepEqual(*sol, wantSol) {
			t.Fatalf("existing sol profile = %#v, want %#v", sol, wantSol)
		}

		before := append([]profile.Profile(nil), got.Profiles...)
		runProfilesCommand(t, "init-codex")
		got, err = profile.Load(profile.DefaultPath())
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Profiles, before) {
			t.Fatalf("second init-codex changed profiles: got %#v, want %#v", got.Profiles, before)
		}
	})

	t.Run("claude", func(t *testing.T) {
		isolateProfiles(t)
		runProfilesCommand(t, "init-claude")
		store, err := profile.Load(profile.DefaultPath())
		if err != nil {
			t.Fatal(err)
		}
		if len(store.Profiles) != 4 {
			t.Fatalf("Claude profiles = %#v, want opus, fable, sonnet, and haiku", store.Profiles)
		}
		for _, name := range []string{"opus", "fable", "sonnet", "haiku"} {
			p, ok := store.Get(name)
			if !ok || p.Harness != profile.HarnessClaudeCode {
				t.Fatalf("missing Claude profile %q in %#v", name, store.Profiles)
			}
		}

		opusProfile, _ := store.Get("opus")
		wantOpus := *opusProfile
		wantOpus.Description = "mine"
		wantOpus.Model = "custom-opus"
		wantOpus.Managed = true
		if err := store.Upsert(wantOpus); err != nil {
			t.Fatal(err)
		}
		if err := store.Save(); err != nil {
			t.Fatal(err)
		}
		runProfilesCommand(t, "init-claude")
		got, err := profile.Load(profile.DefaultPath())
		if err != nil {
			t.Fatal(err)
		}
		if opus, ok := got.Get("opus"); !ok || !reflect.DeepEqual(*opus, wantOpus) {
			t.Fatalf("existing opus profile = %#v, want %#v", opus, wantOpus)
		}
	})

	t.Run("antigravity", func(t *testing.T) {
		isolateProfiles(t)
		runProfilesCommand(t, "init-antigravity")
		store, err := profile.Load(profile.DefaultPath())
		if err != nil {
			t.Fatal(err)
		}
		if len(store.Profiles) != 1 {
			t.Fatalf("Antigravity profiles = %#v, want agy", store.Profiles)
		}
		agy, ok := store.Get("agy")
		if !ok || agy.Harness != profile.HarnessAntigravity || agy.Model != profile.AntigravityModel ||
			len(agy.ExtraArgs) != 0 ||
			agy.Output == nil || agy.Output.Dialect != workeroutput.DialectAcpJSON {
			t.Fatalf("bundled Antigravity profile = %#v", agy)
		}
		before := append([]profile.Profile(nil), store.Profiles...)
		runProfilesCommand(t, "init-antigravity")
		got, err := profile.Load(profile.DefaultPath())
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Profiles, before) {
			t.Fatalf("second init-antigravity changed profiles: got %#v, want %#v", got.Profiles, before)
		}
	})

	t.Run("muse", func(t *testing.T) {
		isolateProfiles(t)
		runProfilesCommand(t, "init-muse")
		store, err := profile.Load(profile.DefaultPath())
		if err != nil {
			t.Fatal(err)
		}
		if len(store.Profiles) != 2 {
			t.Fatalf("Muse profiles = %#v, want wonder and wonder-full", store.Profiles)
		}
		wonder, ok := store.Get("wonder")
		if !ok || wonder.Harness != profile.HarnessMuse || wonder.Model != "muse-wonder-1.0" ||
			wonder.Taste != 9 || wonder.Intelligence != 9 || wonder.Cost != 10 ||
			!reflect.DeepEqual(wonder.ExtraArgs, []string{"--reasoning-effort", "high"}) ||
			wonder.Output == nil || wonder.Output.Dialect != workeroutput.DialectMuseJSONL {
			t.Fatalf("bundled wonder profile = %#v", wonder)
		}
		full, ok := store.Get("wonder-full")
		if !ok || full.Harness != profile.HarnessMuse || full.Model != "muse-wonder-full-1.0" ||
			full.Taste != 10 || full.Intelligence != 10 || full.Cost != 6 ||
			!reflect.DeepEqual(full.ExtraArgs, []string{"--reasoning-effort", "high"}) ||
			full.Output == nil || full.Output.Dialect != workeroutput.DialectMuseJSONL {
			t.Fatalf("bundled wonder-full profile = %#v", full)
		}
		before := append([]profile.Profile(nil), store.Profiles...)
		runProfilesCommand(t, "init-muse")
		got, err := profile.Load(profile.DefaultPath())
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Profiles, before) {
			t.Fatalf("second init-muse changed profiles: got %#v, want %#v", got.Profiles, before)
		}
	})
}

func TestProfilesInitCommandsHaveNoForceAndOldInitIsRemoved(t *testing.T) {
	cmd := NewCommand()
	seen := make(map[string]*cobra.Command)
	for _, subcommand := range cmd.Commands() {
		seen[subcommand.Name()] = subcommand
	}
	if _, ok := seen["init"]; ok {
		t.Fatal("legacy profiles init command is still registered")
	}
	for _, name := range []string{"init-codex", "init-claude", "init-antigravity", "init-muse"} {
		subcommand, ok := seen[name]
		if !ok {
			t.Fatalf("missing %s command", name)
		}
		if flag := subcommand.Flags().Lookup("force"); flag != nil {
			t.Fatalf("%s unexpectedly exposes --force", name)
		}
	}
}

func runProfilesCommand(t *testing.T, args ...string) string {
	t.Helper()
	cmd := NewCommand()
	cmd.SetArgs(args)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	if err := cmd.Execute(); err != nil {
		t.Fatalf("profiles %v: %v", args, err)
	}
	return out.String()
}

func TestSetDefaultRejectsDisabledProfile(t *testing.T) {
	isolateProfiles(t)
	runProfilesCommand(t, "add", "--name", "enabled", "--harness", "mock", "--default")
	runProfilesCommand(t, "add", "--name", "disabled", "--harness", "mock")
	runProfilesCommand(t, "disable", "disabled")

	cmd := NewCommand()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"set-default", "disabled"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "enable the profile") {
		t.Fatalf("set-default error = %v", err)
	}
	s, err := profile.Load(profile.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := s.DefaultProfile(); !ok || p.Name != "enabled" {
		t.Fatalf("default profile = %#v, %v", p, ok)
	}
	runProfilesCommand(t, "enable", "disabled")
	runProfilesCommand(t, "set-default", "disabled")
	runProfilesCommand(t, "remove", "enabled")
	s, err = profile.Load(profile.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := s.DefaultProfile(); !ok || p.Name != "disabled" {
		t.Fatalf("default profile after enabling = %#v, %v", p, ok)
	}
	if _, ok := s.Get("enabled"); ok {
		t.Fatal("removed profile still exists")
	}
}

func TestProfilesListDisabledRequiresAll(t *testing.T) {
	isolateProfiles(t)
	runProfilesCommand(t, "add", "--name", "enabled-profile", "--harness", "mock")
	runProfilesCommand(t, "add", "--name", "disabled-profile", "--harness", "mock")
	runProfilesCommand(t, "disable", "disabled-profile")
	for _, format := range [][]string{nil, {"--json"}} {
		args := append([]string{"list"}, format...)
		visible := runProfilesCommand(t, args...)
		if !strings.Contains(visible, "enabled-profile") || strings.Contains(visible, "disabled-profile") {
			t.Fatalf("default list = %s", visible)
		}
		all := runProfilesCommand(t, append(args, "--all")...)
		if !strings.Contains(all, "disabled-profile") {
			t.Fatalf("all list = %s", all)
		}
		if len(format) == 0 && !strings.Contains(all, "(DISABLED)") {
			t.Fatalf("missing disabled tag: %s", all)
		}
	}
	runProfilesCommand(t, "disable", "enabled-profile")
	empty := runProfilesCommand(t, "list", "--json")
	if strings.TrimSpace(empty) != "[]" {
		t.Fatalf("empty list = %s", empty)
	}
}

func TestProfilesAddModelList(t *testing.T) {
	isolateProfiles(t)
	runProfilesCommand(t, "add", "--name", "chain", "--harness", "mock", "--model", "ignored", "--models", " first ,second", "--models", "third,first")
	s, err := profile.Load(profile.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.Get("chain")
	if p.Model != "first" || !reflect.DeepEqual(p.Models, []string{"first", "second", "third"}) {
		t.Fatalf("stored chain: %+v", p)
	}
	out := runProfilesCommand(t, "list")
	if !strings.Contains(out, "models: first -> second -> third") {
		t.Fatalf("missing chain: %s", out)
	}
	runProfilesCommand(t, "add", "--name", "chain", "--harness", "mock", "--model", "single")
	out = runProfilesCommand(t, "list")
	if !strings.Contains(out, "model: single") || strings.Contains(out, "models:") {
		t.Fatalf("single model list: %s", out)
	}
}

func TestProfilesExplicitModelListTakesPrecedence(t *testing.T) {
	isolateProfiles(t)
	runProfilesCommand(t, "add", "--name", "single", "--harness", "mock",
		"--model", "ignored", "--models", " preferred ,preferred")
	s, err := profile.Load(profile.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.Get("single")
	if p.Model != "preferred" || !reflect.DeepEqual(p.EffectiveModels(), []string{"preferred"}) {
		t.Fatalf("explicit model list lost precedence: %+v", p)
	}
}

func TestAddRejectsHarnessDynaCannotInstall(t *testing.T) {
	isolateProfiles(t)
	previous := health.HarnessReady
	t.Cleanup(func() { health.HarnessReady = previous })
	health.HarnessReady = func(context.Context, string, string) (bool, error) {
		return false, errors.New("Install pi on the Harnesses page first.")
	}
	cmd := NewCommand()
	cmd.SetArgs([]string{"add", "--name", "pi", "--harness", "pi"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil || err.Error() != "Install pi on the Harnesses page first." {
		t.Fatalf("profiles add = %v; want the install hint", err)
	}
}

// A pinned version saves without a bundle: first use installs it.
func TestAddAcceptsPinnedVersionWithoutBundle(t *testing.T) {
	isolateProfiles(t)
	previous := health.HarnessReady
	t.Cleanup(func() { health.HarnessReady = previous })
	health.HarnessReady = harnessruntime.HarnessReady
	runProfilesCommand(t, "add", "--name", "pinned", "--harness", "pi", "--runtime-version", "1.3.0")
}
