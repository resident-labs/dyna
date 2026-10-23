package hooks

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
)

// The self-update hook restarts the engine in the middle of its own run, so no
// result is ever saved and the card stays running. The startup pass ends that
// run, and leaves a run this engine began alone.
func TestSettleInterruptedEndsTheRunAnEarlierEngineLeftRunning(t *testing.T) {
	s, repo := setup(t)
	var posted []room.HookEvent
	s.Post = func(_ Event, ev room.HookEvent) error {
		posted = append(posted, ev)
		return nil
	}
	ctx := t.Context()
	r, err := repoid.Resolve(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	add(t, s, repo, func() Hook {
		updater := hook("update", MainUpdated)
		updater.Label = "Update Dyna"
		return updater
	}())
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	// The run the previous engine started and never finished.
	left, leftLog, err := s.newRun(ctx, r.ID, Hook{Name: "update", Label: "Update Dyna", Event: MainUpdated, Command: "make update", Timeout: "30s"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(leftLog, "installing dyna 7e6caa5c4"); err != nil {
		t.Fatal(err)
	}
	if err := leftLog.Close(); err != nil {
		t.Fatal(err)
	}
	left.StartedAt = now.Add(-time.Hour)
	if _, err := s.saveRun(ctx, left); err != nil {
		t.Fatal(err)
	}

	// A run this engine began, which is still live.
	live, liveLog, err := s.newRun(ctx, r.ID, Hook{Name: "update", Label: "Update Dyna", Event: MainUpdated, Command: "make update", Timeout: "30s"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := liveLog.Close(); err != nil {
		t.Fatal(err)
	}
	live.StartedAt = now
	if _, err := s.saveRun(ctx, live); err != nil {
		t.Fatal(err)
	}

	if err := s.SettleInterrupted(ctx, now); err != nil {
		t.Fatal(err)
	}

	runs, err := s.runs(ctx, r.ID, "update")
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
	byID := map[string]Run{}
	for _, run := range runs {
		byID[run.ID] = run
	}
	settled := byID[left.ID]
	if settled.Result != "error" || !settled.FinishedAt.Equal(now) {
		t.Fatalf("settled run = %+v", settled)
	}
	if byID[live.ID].Result != "pending" {
		t.Fatalf("a live run was settled: %+v", byID[live.ID])
	}
	log, err := os.ReadFile(left.LogPath)
	if err != nil || !strings.Contains(string(log), "the engine stopped during this run") {
		t.Fatalf("log = %q, %v", log, err)
	}
	// The card ends with one typed result: the state, the detail and how long
	// the run lasted.
	if len(posted) != 1 {
		t.Fatalf("posted = %+v", posted)
	}
	ev := posted[0]
	if ev.State != room.HookError || ev.Hook != "update" || ev.Label != "Update Dyna" || ev.RunID != left.ID {
		t.Fatalf("result event = %+v", ev)
	}
	if !strings.Contains(ev.Detail, "the engine stopped during this run") || !strings.Contains(ev.Detail, "installing dyna") {
		t.Fatalf("result detail = %q", ev.Detail)
	}
	if ev.DurationMS != int(time.Hour.Milliseconds()) {
		t.Fatalf("duration = %d", ev.DurationMS)
	}
}
