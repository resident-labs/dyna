package hooks

import (
	"os"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
)

// The self-update hook restarts the engine in the middle of its own run, so no
// result is ever saved and the card stays running. The startup pass ends that
// run, keeps the room its event belongs to, still posts after every hook is
// removed, and leaves a run this engine began alone.
func TestSettleInterruptedEndsTheRunAnEarlierEngineLeftRunning(t *testing.T) {
	s, repo := setup(t)
	posted := map[string]room.HookEvent{} // run ID -> posted result
	rooms := map[string]string{}          // run ID -> room the result went to
	s.Post = func(e Event, ev room.HookEvent) error {
		posted[ev.RunID] = ev
		rooms[ev.RunID] = e.Room
		return nil
	}
	ctx := t.Context()
	r, err := repoid.Resolve(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	updater := hook("update", MainUpdated)
	updater.Label = "Update Dyna"
	add(t, s, repo, updater)
	add(t, s, repo, hook("finish", SessionFinish))
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	start := func(h Hook, e Event, at time.Time, main string) Run {
		t.Helper()
		run, log, err := s.newRun(ctx, r, e, h)
		if err != nil {
			t.Fatal(err)
		}
		if err := log.Close(); err != nil {
			t.Fatal(err)
		}
		run.StartedAt, run.Main = at, main
		if _, err := s.saveRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		return run
	}

	// The run the previous engine started and never finished.
	left := start(Hook{Name: "update", Label: "Update Dyna", Event: MainUpdated, Command: "make update", Timeout: "30s"}, Event{Event: MainUpdated}, now.Add(-time.Hour), r.Main)
	if err := os.WriteFile(left.LogPath, []byte("installing dyna 7e6caa5c4\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// A session-finish run posts in its session's room, not the repository's.
	session := start(Hook{Name: "finish", Event: SessionFinish, Command: "true", Timeout: "30s"}, Event{Event: SessionFinish, Room: "room_session"}, now.Add(-30*time.Minute), r.Main)
	// A run stored before it carried its own checkout.
	legacy := start(Hook{Name: "update", Event: MainUpdated, Command: "true", Timeout: "30s"}, Event{Event: MainUpdated}, now.Add(-2*time.Hour), "")
	// A run this engine began, which is still live.
	live := start(Hook{Name: "update", Event: MainUpdated, Command: "make update", Timeout: "30s"}, Event{Event: MainUpdated}, now, r.Main)

	// Every hook is gone before the engine settles these runs.
	for _, name := range []string{"update", "finish"} {
		if err := s.Remove(ctx, repo, name); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.SettleInterrupted(ctx, now); err != nil {
		t.Fatal(err)
	}

	runs, err := s.runs(ctx, r.ID, "")
	if err != nil || len(runs) != 4 {
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
	// Each card ends with one typed result: the state, the detail and how long
	// the run lasted.
	if len(posted) != 3 {
		t.Fatalf("posted = %+v", posted)
	}
	ev, ok := posted[left.ID]
	if !ok {
		t.Fatalf("no result for the update run: %+v", posted)
	}
	if ev.State != room.HookError || ev.Event != MainUpdated || ev.Hook != "update" || ev.Label != "Update Dyna" || ev.RunID != left.ID {
		t.Fatalf("result event = %+v", ev)
	}
	if !strings.Contains(ev.Detail, "the engine stopped during this run") || !strings.Contains(ev.Detail, "installing dyna") {
		t.Fatalf("result detail = %q", ev.Detail)
	}
	if ev.DurationMS != int(time.Hour.Milliseconds()) {
		t.Fatalf("duration = %d", ev.DurationMS)
	}
	// A session hook's card ends in its own room, and a removed hook still
	// posts the event the run recorded.
	got, ok := posted[session.ID]
	if !ok || rooms[session.ID] != "room_session" {
		t.Fatalf("session result = %+v in room %q", got, rooms[session.ID])
	}
	if got.Event != SessionFinish || got.Hook != "finish" || got.State != room.HookError {
		t.Fatalf("session result event = %+v", got)
	}
	// A run stored without its checkout still reaches the repository's room,
	// because the hook document keeps the main checkout after its last hook.
	if _, ok := posted[legacy.ID]; !ok {
		t.Fatalf("no result for the run without a checkout: %+v", posted)
	}
}
