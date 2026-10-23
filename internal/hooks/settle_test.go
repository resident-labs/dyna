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
//
// The results go through the real room store, so a result the room refuses,
// such as one without an event, fails this test.
func TestSettleInterruptedEndsTheRunAnEarlierEngineLeftRunning(t *testing.T) {
	s, repo := setup(t)
	s.Post = nil // post through the room, as the engine does
	ctx := t.Context()
	r, err := repoid.Resolve(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	rooms := room.DefaultStore()
	repoRoom, err := rooms.EnsureRepoRoom(repo, "hooks")
	if err != nil {
		t.Fatal(err)
	}
	sessionRoom, err := rooms.Create(room.CreateInput{Name: "session-hooks"})
	if err != nil {
		t.Fatal(err)
	}
	updater := hook("update", MainUpdated)
	updater.Label = "Update Dyna"
	add(t, s, repo, updater)
	add(t, s, repo, hook("finish", SessionFinish))
	add(t, s, repo, hook("legacy", MainUpdated))
	add(t, s, repo, hook("legacy-kept", MainUpdated))
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	start := func(h Hook, e Event, at time.Time) Run {
		t.Helper()
		run, log, err := s.newRun(ctx, r, e, h)
		if err != nil {
			t.Fatal(err)
		}
		if err := log.Close(); err != nil {
			t.Fatal(err)
		}
		run.StartedAt = at
		if _, err := s.saveRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		return run
	}

	// The run the previous engine started and never finished.
	left := start(Hook{Name: "update", Label: "Update Dyna", Event: MainUpdated, Command: "make update", Timeout: "30s"}, Event{Event: MainUpdated}, now.Add(-time.Hour))
	if err := os.WriteFile(left.LogPath, []byte("installing dyna 7e6caa5c4\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// A session-finish run posts in its session's room, not the repository's.
	session := start(Hook{Name: "finish", Event: SessionFinish, Command: "true", Timeout: "30s"}, Event{Event: SessionFinish, Room: sessionRoom.Name}, now.Add(-30*time.Minute))
	// A run stored before it kept its event, checkout or room, whose hook is
	// removed too and whose start card is in the session's room. Only that
	// start post still names the event and the room.
	legacy := start(Hook{Name: "legacy", Event: MainUpdated, Command: "true", Timeout: "30s"}, Event{Event: MainUpdated, Session: sessionRoom.Name + ":hook-runner"}, now.Add(-2*time.Hour))
	saved, err := s.runs(ctx, r.ID, "legacy")
	if err != nil || len(saved) != 1 {
		t.Fatalf("legacy run = %+v, %v", saved, err)
	}
	old := saved[0]
	old.Event, old.Label, old.Main, old.Room = "", "", "", ""
	if _, err := s.saveRun(ctx, old); err != nil {
		t.Fatal(err)
	}
	if _, err := rooms.PostEvent(sessionRoom.ID, room.HookEvent{RunID: legacy.ID, Repo: repo, Hook: "legacy", Event: MainUpdated, State: room.HookRunning}, sessionRoom.Name); err != nil {
		t.Fatal(err)
	}
	// A legacy session run whose hook is still configured: its event comes from
	// the hook, but its room still comes from the start card.
	kept := start(Hook{Name: "legacy-kept", Event: MainUpdated, Command: "true", Timeout: "30s"}, Event{Event: MainUpdated, Session: sessionRoom.Name + ":kept-runner"}, now.Add(-90*time.Minute))
	keptSaved, err := s.runs(ctx, r.ID, "legacy-kept")
	if err != nil || len(keptSaved) != 1 {
		t.Fatalf("kept run = %+v, %v", keptSaved, err)
	}
	keptOld := keptSaved[0]
	keptOld.Event, keptOld.Label, keptOld.Main, keptOld.Room = "", "", "", ""
	if _, err := s.saveRun(ctx, keptOld); err != nil {
		t.Fatal(err)
	}
	if _, err := rooms.PostEvent(sessionRoom.ID, room.HookEvent{RunID: kept.ID, Repo: repo, Hook: "legacy-kept", Event: MainUpdated, State: room.HookRunning}, sessionRoom.Name); err != nil {
		t.Fatal(err)
	}
	// A run this engine began, which is still live.
	live := start(Hook{Name: "update", Event: MainUpdated, Command: "make update", Timeout: "30s"}, Event{Event: MainUpdated}, now)
	// A global built-in hook has no repository and no room, so its interrupted
	// run can only end in the hook store that the Hooks screen reads.
	global, globalLog, err := s.newRun(ctx, repoid.Repo{ID: globalKey}, Event{Event: Schedule}, Hook{Name: "maintenance", Label: "Maintenance", Event: Schedule, Timeout: "30s"})
	if err != nil {
		t.Fatal(err)
	}
	if err := globalLog.Close(); err != nil {
		t.Fatal(err)
	}
	global.StartedAt = now.Add(-3 * time.Hour)
	if _, err := s.saveRun(ctx, global); err != nil {
		t.Fatal(err)
	}

	// Every hook is gone before the engine settles these runs.
	for _, name := range []string{"update", "finish", "legacy"} {
		if err := s.Remove(ctx, repo, name); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.SettleInterrupted(ctx, now); err != nil {
		t.Fatal(err)
	}

	runs, err := s.runs(ctx, r.ID, "")
	if err != nil || len(runs) != 5 {
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
	// The global run ends in the hook store, without a room to post in.
	globals, err := s.runs(ctx, globalKey, "")
	if err != nil || len(globals) != 1 {
		t.Fatalf("global runs = %+v, %v", globals, err)
	}
	if globals[0].Result != "error" || !globals[0].FinishedAt.Equal(now) {
		t.Fatalf("global run = %+v", globals[0])
	}
	if log, err := os.ReadFile(global.LogPath); err != nil || !strings.Contains(string(log), "the engine stopped during this run") {
		t.Fatalf("global log = %q, %v", log, err)
	}
	log, err := os.ReadFile(left.LogPath)
	if err != nil || !strings.Contains(string(log), "the engine stopped during this run") {
		t.Fatalf("log = %q, %v", log, err)
	}

	// Each card ends with one typed result the room accepted.
	repoResults := results(t, rooms, repoRoom)
	if len(repoResults) != 1 {
		t.Fatalf("repository results = %+v", repoResults)
	}
	ev, ok := repoResults[left.ID]
	if !ok {
		t.Fatalf("no result for the update run: %+v", repoResults)
	}
	if ev.State != room.HookError || ev.Event != MainUpdated || ev.Hook != "update" || ev.Label != "Update Dyna" {
		t.Fatalf("result event = %+v", ev)
	}
	if !strings.Contains(ev.Detail, "the engine stopped during this run") || !strings.Contains(ev.Detail, "installing dyna") {
		t.Fatalf("result detail = %q", ev.Detail)
	}
	if ev.DurationMS != int(time.Hour.Milliseconds()) {
		t.Fatalf("duration = %d", ev.DurationMS)
	}
	// A session hook's card ends in its own room.
	sessionResults := results(t, rooms, sessionRoom)
	if got, ok := sessionResults[session.ID]; !ok || got.Event != SessionFinish || got.Hook != "finish" || got.State != room.HookError {
		t.Fatalf("session results = %+v", sessionResults)
	}
	// A legacy session run with no metadata ends in the session room its start
	// card is in, with the event that card named.
	recovered, ok := sessionResults[legacy.ID]
	if !ok || recovered.Event != MainUpdated || recovered.Hook != "legacy" || recovered.State != room.HookError {
		t.Fatalf("recovered result = %+v, %+v", recovered, sessionResults)
	}
	if _, ok := sessionResults[left.ID]; ok {
		t.Fatal("the repository run ended in the session's room")
	}
	// A legacy session run whose hook is still configured ends in its session
	// room, not the repository's.
	if got, ok := sessionResults[kept.ID]; !ok || got.Event != MainUpdated || got.Hook != "legacy-kept" || got.State != room.HookError {
		t.Fatalf("kept-hook result = %+v, %+v", got, sessionResults)
	}
}

// results reads a room's newest hook result for each run.
func results(t *testing.T, rooms *room.Store, rec room.Record) map[string]room.HookEvent {
	t.Helper()
	msgs, err := rooms.Messages(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]room.HookEvent{}
	for _, m := range msgs {
		h, ok := m.Hook()
		if ok && h.State != room.HookRunning {
			out[h.RunID] = h
		}
	}
	return out
}
