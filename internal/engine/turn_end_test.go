package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

// A bound session's turn end fires the session-turn-ended hook event for that
// session's repository, so a job that reacts to a turn starts at once instead
// of at its next interval.
func TestTurnEndFiresSessionTurnEndedHookEvent(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "--allow-empty", "-m", "initial"},
	} {
		if _, err := repoid.Git(t.Context(), repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	hooksService := hooks.Service{DataDir: t.TempDir(), Post: func(hooks.Event, room.HookEvent) error { return nil }}
	hook := hooks.Hook{Name: "on-turn-end", Event: hooks.SessionTurnEnded, Command: "true", Cwd: repo, Timeout: "30s", OnFail: "alert"}
	if err := hooksService.Add(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), repo, hook); err != nil {
		t.Fatal(err)
	}
	sessions := &subagent.Store{Root: t.TempDir()}
	const id = "ses_20261023-120000-abcdefgh"
	recordtest.Write(t, sessions.Root, id, subagent.Meta{ID: id, Name: "worker", RoomID: "room_one", Room: "work", Dir: repo, Profile: "sol"})

	signals := newWorkerSignals(nil)
	// Two turns of the same session before one hooks pass fire one event.
	signals.wake(roomsvc.Event{Kind: "posts", Posts: []roomsvc.Post{
		{Message: room.Message{Kind: "completed", Turn: 1, From: "work:worker", SessionIDs: map[string]string{"work:worker": id}}},
		{Message: room.Message{Kind: "completed", Turn: 2, From: "work:worker", SessionIDs: map[string]string{"work:worker": id}}},
	}})
	select {
	case <-signals.channels["hooks"]:
	default:
		t.Fatal("a turn end did not wake the hooks worker")
	}
	if err := fireTurnEnds(t.Context(), sessions, hooksService, signals.takeTurnEnds()); err != nil {
		t.Fatal(err)
	}
	events := queuedHookEvents(t, filepath.Join(hooksService.DataDir, "hook-spool"))
	if len(events) != 1 {
		t.Fatalf("queued %d hook events, want 1: %+v", len(events), events)
	}
	got := events[0]
	if got.Event != hooks.SessionTurnEnded || got.Session != "work:worker" || got.Room != "room_one" || !strings.HasSuffix(got.Repo, filepath.Base(repo)) {
		t.Fatalf("queued event = %+v", got)
	}
}

// queuedHookEvents reads the events that a hook spool holds.
func queuedHookEvents(t *testing.T, dir string) []hooks.Event {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var events []hooks.Event
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var e hooks.Event
		if err := json.Unmarshal(raw, &e); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	return events
}
