package doctoralert

import (
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/doctor"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

var t0 = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func fake(t *testing.T, rooms []RoomUse) (*Notifier, *[]Note, *time.Time) {
	now := t0
	var sent []Note
	return &Notifier{
		DataDir: t.TempDir(),
		Now:     func() time.Time { return now },
		Use:     func() ([]RoomUse, error) { return rooms, nil },
		Send:    func(n Note) error { sent = append(sent, n); return nil },
	}, &sent, &now
}

func TestEachRoomWithAgentsHearsOncePerIntervalThenRecovery(t *testing.T) {
	n, sent, now := fake(t, []RoomUse{{RoomID: "a", Room: "a", Running: 2, Idle: 29}, {RoomID: "b", Room: "b", Running: 4}})
	rep := doctor.Report{Machine: "laptop"}
	if err := n.Alert(rep); err != nil || len(*sent) != 2 {
		t.Fatalf("first alert sent %d, %v", len(*sent), err)
	}
	first := (*sent)[0]
	if first.Total != 35 || !strings.Contains(first.Advice(), "this room runs 31 of 35 agent sessions") || !strings.Contains(first.Advice(), "Close the 29 idle") {
		t.Fatalf("advice = %q total %d", first.Advice(), first.Total)
	}
	if got := (*sent)[1].Advice(); !strings.Contains(got, "Pause or stop 2 of the 4 running") {
		t.Fatalf("running-only advice = %q", got)
	}
	*now = t0.Add(Interval - time.Minute)
	if err := n.Alert(rep); err != nil || len(*sent) != 2 {
		t.Fatalf("alert inside the interval sent %d, %v", len(*sent), err)
	}
	*now = t0.Add(Interval)
	if err := n.Alert(rep); err != nil || len(*sent) != 4 {
		t.Fatalf("alert after the interval sent %d, %v", len(*sent), err)
	}
	if err := n.Recover("laptop"); err != nil || len(*sent) != 6 || !(*sent)[5].Recovered {
		t.Fatalf("recovery sent %d, %v", len(*sent), err)
	}
	if err := n.Recover("laptop"); err != nil || len(*sent) != 6 {
		t.Fatalf("second recovery sent %d, %v", len(*sent), err)
	}
	if err := n.Alert(rep); err != nil || len(*sent) != 8 {
		t.Fatalf("a new overload must alert at once, sent %d, %v", len(*sent), err)
	}
}

func TestCountSkipsClosedDormantAndRoomless(t *testing.T) {
	mk := func(room, status string) subagent.Info {
		return subagent.Info{Meta: subagent.Meta{RoomID: room, Room: room}, Status: status}
	}
	got := Count([]subagent.Info{mk("a", subagent.StatusRunning), mk("a", subagent.StatusIdle), mk("a", subagent.StatusDormant), mk("b", subagent.StatusClosed), mk("", subagent.StatusIdle)})
	if len(got) != 1 || got[0].Running != 1 || got[0].Idle != 1 {
		t.Fatalf("count = %+v", got)
	}
}

func TestPostNoteWakesRootForAnOverloadButNotForRecovery(t *testing.T) {
	store := &room.Store{Root: t.TempDir()}
	rec, err := store.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	use := RoomUse{RoomID: rec.ID, Room: rec.Name, Running: 1, Idle: 3}
	rep := doctor.Report{ID: 7, Machine: "laptop", Summary: "laptop: load 3.0/core", Findings: []doctor.Finding{{Cause: "session:ses_a uses 5.0 cores", Command: "dyna subagent show ses_a"}}}
	if err := PostNote(store, Note{Room: use, Machine: "laptop", Total: 9, Report: rep}); err != nil {
		t.Fatal(err)
	}
	if err := PostNote(store, Note{Room: use, Machine: "laptop", Recovered: true}); err != nil {
		t.Fatal(err)
	}
	posts, err := store.Messages(rec.ID)
	if err != nil || len(posts) != 2 {
		t.Fatalf("posts = %+v, %v", posts, err)
	}
	alert, ok := posts[0].Alert()
	if !ok || !posts[0].Wake || alert.NextCommand != "dyna subagent show ses_a" || !strings.Contains(alert.Detail, "this room runs 4 of 9") {
		t.Fatalf("overload post = %+v %+v", posts[0], alert)
	}
	if posts[1].Wake {
		t.Fatalf("recovery woke the root: %+v", posts[1])
	}
}
