package profiles

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

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

func TestProfilesAddDiagnostics(t *testing.T) {
	isolateProfiles(t)
	runProfilesCommand(t, "add", "--name", "rated", "--harness", "mock", "--diagnostics", "9")
	store, err := profile.Load(profile.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := store.Get("rated"); !ok || p.Diagnostics != 9 {
		t.Fatalf("saved profile = %+v, want diagnostics 9", p)
	}
	if list := runProfilesCommand(t, "list"); !strings.Contains(list, "diagnostics: 9/10") {
		t.Fatalf("list output missing diagnostics: %s", list)
	}
	// A plain add keeps the neutral default of 5, and an old profile shows it.
	runProfilesCommand(t, "add", "--name", "plain", "--harness", "mock")
	plain, _ := profile.Load(profile.DefaultPath())
	if q, _ := plain.Get("plain"); q.Diagnostics != 5 {
		t.Fatalf("default diagnostics = %d, want 5", q.Diagnostics)
	}
	raw, err := os.ReadFile(profile.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]any
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	// Drop the key, as a profile saved before the rating existed would lack it.
	for _, item := range file["profiles"].([]any) {
		delete(item.(map[string]any), "diagnostics")
	}
	if raw, err = json.Marshal(file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profile.DefaultPath(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	list := runProfilesCommand(t, "list", "--all", "--json")
	if !strings.Contains(list, `"diagnostics": 5`) {
		t.Fatalf("list --json did not show the neutral rating: %s", list)
	}
	var one struct {
		Diagnostics int `json:"diagnostics"`
	}
	if err := json.Unmarshal([]byte(runProfilesCommand(t, "show", "plain")), &one); err != nil {
		t.Fatal(err)
	}
	if one.Diagnostics != 5 {
		t.Fatalf("show diagnostics = %d, want 5", one.Diagnostics)
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

func TestProfilesCommandRegistersNoSeedingCommands(t *testing.T) {
	cmd := NewCommand()
	for _, subcommand := range cmd.Commands() {
		if strings.HasPrefix(subcommand.Name(), "init") {
			t.Errorf("profiles %s seeds profiles; the owner adds every profile", subcommand.Name())
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
