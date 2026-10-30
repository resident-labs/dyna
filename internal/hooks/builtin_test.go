package hooks

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// autoCheck is a test table entry: it names what it saw in the run log, so a
// test can follow the pass without a subprocess.
func autoCheck(seen *[]Builtin) BuiltinSpec {
	return BuiltinSpec{
		Builtin: "auto-check", Label: "Auto check", EveryMinutes: 15, Timeout: "30s",
		Run: func(_ context.Context, _ int, log io.Writer) error {
			*seen = append(*seen, "auto-check")
			_, err := fmt.Fprintln(log, "auto check ran")
			return err
		},
	}
}

// A built-in table entry runs through the same run path that
// `dyna services run <name>` calls, and stores a run and a log.
func TestEveryBuiltinRunsThroughTheRunCommand(t *testing.T) {
	s, _ := setup(t)
	var seen []Builtin
	s.Builtins = []BuiltinSpec{autoCheck(&seen)}
	if err := s.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, spec := range s.Builtins {
		run, err := s.RunBuiltin(t.Context(), Event{Event: Schedule}, string(spec.Builtin))
		if err != nil {
			t.Fatal(err)
		}
		if run.Result != "pass" || run.Repo != globalKey || run.Name != "hook-"+string(spec.Builtin) {
			t.Fatalf("run = %+v", run)
		}
		log, err := os.ReadFile(run.LogPath)
		if err != nil || !strings.Contains(string(log), "auto check ran") {
			t.Fatalf("log = %q, %v", log, err)
		}
		runs, err := s.Runs(t.Context(), "", string(spec.Builtin))
		if err != nil || len(runs) != 1 || runs[0].ID != run.ID {
			t.Fatalf("stored runs = %+v, %v", runs, err)
		}
	}
	if len(seen) != len(s.Builtins) {
		t.Fatalf("table passes run = %v", seen)
	}
}

// A built-in's action is the engine's: the owner may change its interval,
// timeout and switch, and can turn it back to the seeded defaults.
func TestBuiltinRowCannotBeRemovedOrRetargeted(t *testing.T) {
	s, _ := setup(t)
	var seen []Builtin
	s.Builtins = []BuiltinSpec{autoCheck(&seen)}
	if err := s.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	shorter, alert := "10s", "block"
	for _, patch := range []struct {
		name  string
		patch Patch
	}{
		{"command", Patch{Command: &shorter}},
		{"event", Patch{Event: &alert}},
		{"cwd", Patch{Cwd: &shorter}},
	} {
		err := s.Edit(ctx, "", "auto-check", "owner", patch.patch)
		if err == nil || !strings.Contains(err.Error(), "its action cannot change") {
			t.Fatalf("%s edit = %v", patch.name, err)
		}
	}
	if err := s.Remove(ctx, "", "auto-check"); err == nil || !strings.Contains(err.Error(), "cannot be removed") {
		t.Fatalf("remove = %v", err)
	}
	minutes, off := 90, true
	if err := s.Edit(ctx, "", "auto-check", "owner", Patch{EveryMinutes: &minutes, Disabled: &off}); err != nil {
		t.Fatal(err)
	}
	hooks, err := s.BuiltinHooks(ctx)
	if err != nil || len(hooks) != 1 || hooks[0].EveryMinutes != 90 || !hooks[0].Disabled {
		t.Fatalf("edited row = %+v, %v", hooks, err)
	}
	if err := s.Reset(ctx, "auto-check", "owner"); err != nil {
		t.Fatal(err)
	}
	hooks, err = s.BuiltinHooks(ctx)
	if err != nil || len(hooks) != 1 {
		t.Fatalf("reset rows = %+v, %v", hooks, err)
	}
	h := hooks[0]
	if h.EveryMinutes != 15 || h.Timeout != "30s" || h.Disabled || h.Command != "dyna services run auto-check" {
		t.Fatalf("reset row = %+v", h)
	}
}

// Seeding is idempotent: the owner's interval, timeout and switch stay, the
// fixed fields come back, and a built-in this release no longer has is
// removed with its function.
func TestSeedKeepsOwnerSettingsAndRetiresUnknownBuiltin(t *testing.T) {
	s, _ := setup(t)
	var seen []Builtin
	s.Builtins = []BuiltinSpec{autoCheck(&seen)}
	ctx := t.Context()
	if err := s.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	minutes, off := 45, true
	if err := s.Edit(ctx, "", "auto-check", "owner", Patch{EveryMinutes: &minutes, Disabled: &off}); err != nil {
		t.Fatal(err)
	}
	if err := s.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	hooks, err := s.BuiltinHooks(ctx)
	if err != nil || len(hooks) != 1 {
		t.Fatalf("seeded twice = %+v, %v", hooks, err)
	}
	if h := hooks[0]; h.EveryMinutes != 45 || !h.Disabled || h.Event != Schedule || h.Label != "Auto check" {
		t.Fatalf("second seed changed the row: %+v", h)
	}
	if hooks[0].AddedAt.IsZero() {
		t.Fatal("seeded row has no creation time")
	}
	s.Builtins = nil
	if err := s.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	if hooks, err := s.BuiltinHooks(ctx); err != nil || len(hooks) != 0 {
		t.Fatalf("retired row = %+v, %v", hooks, err)
	}
}

// Global hooks belong to no repository, so they run on their own tick and not
// through a repository's.
func TestGlobalScheduleTicksApartFromRepositories(t *testing.T) {
	s, repo := setup(t)
	var seen []Builtin
	s.Builtins = []BuiltinSpec{autoCheck(&seen)}
	ctx := t.Context()
	if err := s.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	add(t, s, repo, hook("repo-hook", Schedule).withEvery(60))
	start := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	if err := s.Tick(ctx, repo, start); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 0 {
		t.Fatalf("a repository tick ran a global hook: %v", seen)
	}
	if err := s.TickGlobal(ctx, start); err != nil {
		t.Fatal(err)
	}
	if err := s.TickGlobal(ctx, start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 {
		t.Fatalf("global hook runs in two minutes = %v", seen)
	}
	if err := s.TickGlobal(ctx, start.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 {
		t.Fatalf("global hook did not run at its interval: %v", seen)
	}
}

// A global hook the owner adds needs an absolute directory, because it has no
// checkout to run in.
func TestGlobalUserHookNeedsAbsoluteCwd(t *testing.T) {
	s, _ := setup(t)
	ctx := t.Context()
	h := hook("nightly", Schedule).withEvery(60)
	if err := s.AddGlobal(ctx, h); err == nil || !strings.Contains(err.Error(), "absolute path") {
		t.Fatalf("relative cwd = %v", err)
	}
	h.Cwd = t.TempDir()
	if err := s.AddGlobal(ctx, h); err != nil {
		t.Fatal(err)
	}
	hooks, err := s.ListGlobal(ctx)
	if err != nil || len(hooks) != 1 || hooks[0].Scope != ScopeGlobal || hooks[0].Builtin != "" {
		t.Fatalf("global rows = %+v, %v", hooks, err)
	}
	if _, err := s.List(ctx, t.TempDir()); err == nil {
		t.Fatal("a directory without a repository listed hooks")
	}
}

// A built-in's interval is the owner's to set, and a minute is its floor, so a
// zero or negative interval is refused.
func TestBuiltinIntervalCannotGoBelowOneMinute(t *testing.T) {
	s, _ := setup(t)
	var seen []Builtin
	s.Builtins = []BuiltinSpec{autoCheck(&seen)}
	if err := s.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, minutes := range []int{0, -5} {
		if err := s.Edit(t.Context(), "", "auto-check", "owner", Patch{EveryMinutes: &minutes}); err == nil || !strings.Contains(err.Error(), "positive every-minutes") {
			t.Fatalf("everyMinutes=%d = %v", minutes, err)
		}
	}
	hooks, err := s.BuiltinHooks(t.Context())
	if err != nil || len(hooks) != 1 || hooks[0].EveryMinutes != 15 {
		t.Fatalf("row after a refused edit = %+v, %v", hooks, err)
	}
}

// A global hook the owner added earlier with a built-in's name keeps running
// under a free name, so seeding never stores one name twice.
func TestSeedRenamesAnOwnerHookThatHoldsABuiltinName(t *testing.T) {
	s, repo := setup(t)
	var seen []Builtin
	s.Builtins = []BuiltinSpec{autoCheck(&seen)}
	ctx := t.Context()
	owned := hook("auto-check", Schedule).withEvery(15)
	owned.Cwd = t.TempDir()
	owned.Scope = ScopeGlobal
	owned.AddedBy, owned.AddedAt = "owner", time.Now().UTC()
	// An earlier release stored this row before the name was reserved.
	if err := s.update(ctx, globalKey, func(d *document) error {
		d.Hooks = append(d.Hooks, owned)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	hooks, err := s.load(ctx, globalKey)
	if err != nil || len(hooks) != 2 {
		t.Fatalf("rows after seed = %+v, %v", hooks, err)
	}
	byName := map[string]Hook{}
	for _, h := range hooks {
		byName[h.Name] = h
	}
	if byName["auto-check"].Builtin != "auto-check" {
		t.Fatalf("the built-in lost its name: %+v", byName["auto-check"])
	}
	if byName["auto-check-user"].Builtin != "" {
		t.Fatalf("the owner's hook was not renamed: %+v", byName)
	}
	// The reserved name is refused for a new global hook, and allowed for a
	// repository hook, which belongs to another scope.
	fresh := hook("auto-check", Schedule).withEvery(15)
	fresh.Cwd = t.TempDir()
	if err := s.AddGlobal(ctx, fresh); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("reserved global name = %v", err)
	}
	add(t, s, repo, hook("auto-check", AfterLand))
}

// withEvery sets the interval of a schedule hook under test.
func (h Hook) withEvery(minutes int) Hook {
	h.Event, h.EveryMinutes = Schedule, minutes
	return h
}

// A built-in is a service: the hook list and the by-hand hook run leave it
// out, while the service list and `dyna services run` keep it.
func TestBuiltinLeavesTheHookList(t *testing.T) {
	s, _ := setup(t)
	var seen []Builtin
	s.Builtins = []BuiltinSpec{autoCheck(&seen)}
	ctx := t.Context()
	if err := s.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	owned := hook("mine", Schedule).withEvery(15)
	owned.Cwd = t.TempDir()
	if err := s.AddGlobal(ctx, owned); err != nil {
		t.Fatal(err)
	}
	if listed, err := s.ListGlobal(ctx); err != nil || len(listed) != 1 || listed[0].Name != "mine" {
		t.Fatalf("global hooks = %+v, %v", listed, err)
	}
	if summaries, err := s.SummariesGlobal(ctx); err != nil || len(summaries) != 1 || summaries[0].Hook.Name != "mine" {
		t.Fatalf("global summaries = %+v, %v", summaries, err)
	}
	if summaries, err := s.SummariesBuiltin(ctx); err != nil || len(summaries) != 1 || summaries[0].Hook.Name != "auto-check" {
		t.Fatalf("service summaries = %+v, %v", summaries, err)
	}
	if _, err := s.RunGlobal(ctx, Event{}, "auto-check"); err == nil || !strings.Contains(err.Error(), "dyna services run auto-check") {
		t.Fatalf("hook run of a service = %v", err)
	}
	if _, err := s.RunBuiltin(ctx, Event{}, "mine"); err == nil {
		t.Fatal("service run of an owner hook succeeded")
	}
	if len(seen) != 0 {
		t.Fatalf("a refused run still ran the pass: %v", seen)
	}
}
