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
		Run: func(_ context.Context, log io.Writer) error {
			*seen = append(*seen, "auto-check")
			_, err := fmt.Fprintln(log, "auto check ran")
			return err
		},
	}
}

// A built-in table entry runs through the same run path that
// `dyna git hooks run <name> --global` calls, and stores a run and a log.
func TestEveryBuiltinRunsThroughTheRunCommand(t *testing.T) {
	s, _ := setup(t)
	var seen []Builtin
	s.Builtins = []BuiltinSpec{autoCheck(&seen)}
	if err := s.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, spec := range s.Builtins {
		run, err := s.RunGlobal(t.Context(), Event{Event: Schedule}, string(spec.Builtin))
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
	hooks, err := s.ListGlobal(ctx)
	if err != nil || len(hooks) != 1 || hooks[0].EveryMinutes != 90 || !hooks[0].Disabled {
		t.Fatalf("edited row = %+v, %v", hooks, err)
	}
	if err := s.Reset(ctx, "auto-check", "owner"); err != nil {
		t.Fatal(err)
	}
	hooks, err = s.ListGlobal(ctx)
	if err != nil || len(hooks) != 1 {
		t.Fatalf("reset rows = %+v, %v", hooks, err)
	}
	h := hooks[0]
	if h.EveryMinutes != 15 || h.Timeout != "30s" || h.Disabled || h.Command != "dyna git hooks run auto-check --global" {
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
	hooks, err := s.ListGlobal(ctx)
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
	if hooks, err := s.ListGlobal(ctx); err != nil || len(hooks) != 0 {
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

// withEvery sets the interval of a schedule hook under test.
func (h Hook) withEvery(minutes int) Hook {
	h.Event, h.EveryMinutes = Schedule, minutes
	return h
}
