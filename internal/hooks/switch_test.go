package hooks

import (
	"context"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/processguard"
	"dyna-agent/internal/repoid"
)

// A disabled hook keeps its settings and runs nothing: the switch is not a
// delete.
func TestDisabledHookDoesNotRun(t *testing.T) {
	s, repo := setup(t)
	ctx := context.Background()
	s.Exec = func(context.Context, processguard.Process) error { return nil }
	add(t, s, repo, hook("keep", SessionFinish))
	off := hook("skip", SessionFinish)
	add(t, s, repo, off)
	disabled := true
	if err := s.Edit(ctx, repo, off.Name, "owner", Patch{Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if err := s.Emit(ctx, Event{Event: SessionFinish, Repo: repo, Name: off.Name}); err != nil {
		t.Fatal(err)
	}
	if events := queuedEvents(t, s); len(events) != 0 {
		t.Fatalf("disabled hook queued events: %+v", events)
	}
	if err := s.Dispatch(ctx, Event{Event: SessionFinish, Repo: repo}); err != nil {
		t.Fatal(err)
	}
	r, err := repoid.Resolve(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if runs, err := s.runs(ctx, r.ID, "skip"); err != nil || len(runs) != 0 {
		t.Fatalf("a disabled hook ran %d times: %#v err=%v", len(runs), runs, err)
	}
	if runs, err := s.runs(ctx, r.ID, "keep"); err != nil || len(runs) != 1 {
		t.Fatalf("the enabled hook ran %d times: %#v err=%v", len(runs), runs, err)
	}
	list, err := s.List(ctx, repo)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %#v %v", list, err)
	}
	for _, h := range list {
		if h.Name != "skip" {
			continue
		}
		if !h.Disabled || h.Command != off.Command || h.Timeout != off.Timeout {
			t.Fatalf("the disabled hook lost its settings: %#v", h)
		}
	}
}

// A disabled schedule hook is never claimed, so it queues no work and its
// interval does not advance.
func TestDisabledScheduleHookIsNotClaimed(t *testing.T) {
	s, repo := setup(t)
	ctx := context.Background()
	s.Fetch = func(context.Context, string) error { return nil }
	scheduled := func(name string, disabled bool) Hook {
		h := hook(name, Schedule)
		h.EveryMinutes, h.Disabled = 10, disabled
		return h
	}
	add(t, s, repo, scheduled("on", false))
	add(t, s, repo, scheduled("off", true))
	if err := s.Tick(ctx, repo, time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	files, err := spoolFiles(s.spoolDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("the spool holds %d events; want 1, from the enabled hook only", len(files))
	}
	disabled := false
	if err := s.Edit(ctx, repo, "off", "owner", Patch{Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(ctx, repo, time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if events := queuedEvents(t, s); len(events) != 2 {
		t.Fatalf("re-enabled schedule should run immediately: %+v", events)
	}
}

// Summaries returns one row per hook, in saved order, with the newest run.
func TestSummariesReturnNewestRunPerHook(t *testing.T) {
	s, repo := setup(t)
	ctx := context.Background()
	add(t, s, repo, hook("one", SessionFinish))
	off := hook("two", SessionFinish)
	off.Disabled = true
	add(t, s, repo, off)
	r, err := repoid.Resolve(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	older := Run{ID: strings.Repeat("a", 32), Repo: r.ID, Name: "hook-one", Result: "pass", StartedAt: now.Add(-time.Hour), FinishedAt: now.Add(-time.Hour)}
	newer := Run{ID: strings.Repeat("b", 32), Repo: r.ID, Name: "hook-one", Result: "error", StartedAt: now, FinishedAt: now}
	for _, run := range []Run{newer, older} {
		if _, err := s.saveRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Summaries(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Hook.Name != "one" || got[1].Hook.Name != "two" {
		t.Fatalf("summaries: %#v", got)
	}
	if got[0].LastRun == nil || got[0].LastRun.ID != newer.ID || got[0].LastRun.Result != "error" {
		t.Fatalf("newest run for hook one: %#v", got[0].LastRun)
	}
	if got[1].LastRun != nil {
		t.Fatalf("hook two has no runs, but got %#v", got[1].LastRun)
	}
	if !got[1].Hook.Disabled {
		t.Fatalf("the disabled flag did not reach the summary: %#v", got[1].Hook)
	}
}
