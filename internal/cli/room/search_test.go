package room

import (
	"bytes"
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
// turn. A turn that wrote no output still matches on its stored prompt, a
// message that is only the searched word still matches, and a session closed
// outside the 14-day window is not scanned.
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
	// writeSession stores one closed session with one finished turn. output is
	// the stored normalized output, empty when the harness wrote none.
	writeSession := func(id, name string, closed time.Time, prompt, output string) {
		t.Helper()
		start := closed.Add(-time.Hour)
		recordtest.Write(t, sessions.Root, id, subagent.Meta{ID: id, Name: name, Room: work.Name, RoomID: work.ID,
			Profile: "sol", Depth: 1, Turns: 1, Created: start, LastActivity: closed, Status: subagent.StatusClosed, Closed: closed})
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
		if output != "" {
			path, err := sessions.NormalizedPath(id)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(output), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		turn, err := json.Marshal(subagent.Turn{N: 1, Status: subagent.TurnOK, Profile: "sol", Prompt: prompt,
			NormStart: 0, NormEnd: int64(len(output)), Started: start, Ended: closed})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "turns", "1.json"), turn, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	writeSession("ses_20261024-100000-aaaaaaaa", "recent", now, "finish the task", `{"v":1,"kind":"message","role":"assistant","text":"the deploy gate stays shut until CI passes","ts":1792853630100}`+"\n")
	writeSession("ses_20261024-100001-bbbbbbbb", "whole", now, "finish the task", `{"v":1,"kind":"message","role":"assistant","text":"quasar","ts":1792853630100}`+"\n")
	writeSession("ses_20261024-100002-cccccccc", "prompt", now, "prompthit: no output was written", "")
	writeSession("ses_20261001-100000-dddddddd", "old", now.Add(-20*24*time.Hour), "the deploy gate stays shut until CI passes", "")

	for _, tc := range []struct {
		name          string
		args          []string
		want, notWant []string
	}{
		{"post and output turn", []string{"search", "deploy", "gate", "--room", "work", "--sessions"},
			[]string{hit.ID, "the deploy gate waits for CI", "work:recent 1", "the deploy gate stays shut until CI passes"},
			[]string{"unrelated chatter", "work:old"}},
		{"message holding only the word", []string{"search", "quasar", "--room", "work", "--sessions"},
			[]string{"work:whole 1"}, []string{"no matches"}},
		{"turn with no output", []string{"search", "prompthit", "--room", "work", "--sessions"},
			[]string{"work:prompt 1", "prompthit: no output was written"}, []string{"no matches"}},
		{"posts only", []string{"search", "deploy", "gate", "--room", "work"},
			[]string{hit.ID}, []string{"turn  work:"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := execCmd(t, RoomCmd(), tc.args...)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Fatalf("missing %q: %s", want, out)
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(out, notWant) {
					t.Fatalf("unexpected %q: %s", notWant, out)
				}
			}
		})
	}
}

// A bounded transcript scan that found no line says so, and never claims that
// the room holds no match.
func TestRoomSearchReportsAnIncompleteScan(t *testing.T) {
	var out bytes.Buffer
	found := turnSearch{stopped: true}
	if err := printSearch(&out, room.Record{Name: "work", ID: "room_work"}, "quasar", nil, false, found, true); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "stopped after") {
		t.Fatalf("the incomplete scan is not reported: %s", text)
	}
	if strings.Contains(text, "no matches") {
		t.Fatalf("an incomplete scan claims no matches: %s", text)
	}
}
