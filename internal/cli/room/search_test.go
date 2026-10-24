package room

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

// Search cites only its sources: a post ID, and with --sessions a session and
// turn. A session closed outside the 14-day window is not part of the scan.
func TestRoomSearchCitesPostsAndRecentTurns(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	clearStamp(t)
	work := workRoom(t)
	store := room.DefaultStore()
	hit, err := store.Post(work.ID, room.PostInput{From: "work:lead", Kind: "message", To: []string{"work"}, Text: "the deploy gate waits for CI"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Post(work.ID, room.PostInput{From: "work:lead", Kind: "message", To: []string{"work"}, Text: "unrelated chatter"}); err != nil {
		t.Fatal(err)
	}
	sessions := subagent.DefaultStore()
	writeSession := func(id, name string, closed time.Time) {
		t.Helper()
		start := closed.Add(-time.Hour)
		recordtest.Write(t, sessions.Root, id, subagent.Meta{ID: id, Name: name, Room: work.Name, RoomID: work.ID,
			Profile: "sol", Depth: 1, Turns: 1, Created: start, LastActivity: closed, Status: subagent.StatusClosed, Closed: closed})
		output := `{"v":1,"kind":"message","role":"assistant","text":"the deploy gate stays shut until CI passes","ts":1792853630100}` + "\n"
		dir, err := sessions.Dir(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "turns"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "agents", "1"), 0o700); err != nil {
			t.Fatal(err)
		}
		path, err := sessions.NormalizedPath(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(output), 0o600); err != nil {
			t.Fatal(err)
		}
		turn, err := json.Marshal(subagent.Turn{N: 1, Status: subagent.TurnOK, Profile: "sol",
			NormStart: 0, NormEnd: int64(len(output)), Started: start, Ended: closed})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "turns", "1.json"), turn, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	writeSession("ses_20261024-100000-aaaaaaaa", "recent", now)
	writeSession("ses_20261001-100000-bbbbbbbb", "old", now.Add(-20*24*time.Hour))

	out, err := execCmd(t, RoomCmd(), "search", "deploy", "gate", "--room", "work", "--sessions")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, hit.ID) || !strings.Contains(out, "the deploy gate waits for CI") {
		t.Fatalf("post hit missing: %s", out)
	}
	if !strings.Contains(out, "work:recent 1") || !strings.Contains(out, "the deploy gate stays shut until CI passes") {
		t.Fatalf("turn hit missing: %s", out)
	}
	if strings.Contains(out, "unrelated chatter") {
		t.Fatalf("a post without every word matched: %s", out)
	}
	if strings.Contains(out, "work:old") {
		t.Fatalf("a session closed outside the window was searched: %s", out)
	}

	postsOnly, err := execCmd(t, RoomCmd(), "search", "deploy", "gate", "--room", "work")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(postsOnly, "turn ") || !strings.Contains(postsOnly, hit.ID) {
		t.Fatalf("without --sessions search must read only posts: %s", postsOnly)
	}
}
