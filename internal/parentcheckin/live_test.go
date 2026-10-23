package parentcheckin_test

import (
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/engine/stores"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/parentcheckin"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

func TestLiveCheckInStoresOnePostAcrossChangedSummaryRetry(t *testing.T) {
	root := storetest.Isolate(t)
	if err := stores.Migrate(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(root) })
	rooms := room.DefaultStore()
	rec, err := rooms.Create(room.CreateInput{Name: "dyna"})
	if err != nil {
		t.Fatal(err)
	}
	sessions := subagent.DefaultStore()
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	parent := subagent.Meta{ID: subagent.NewID(), Name: "root", Role: subagent.RoleRoot, RoomID: rec.ID, Room: rec.Name, Status: subagent.StatusIdle, Created: now}
	child := subagent.Meta{ID: subagent.NewID(), Name: "child", ParentSession: parent.ID, RoomID: rec.ID, Room: rec.Name, Status: subagent.StatusIdle, Created: now, LastActivity: now}
	recordtest.Write(t, sessions.Root, parent.ID, parent)
	recordtest.Write(t, sessions.Root, child.ID, child)
	s := parentcheckin.New(notify.New(rooms, &subagent.Manager{Store: sessions}))
	s.Now = func() time.Time { return now }
	s.Interval = func() (time.Duration, error) { return time.Minute, nil }
	if _, err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	messages, err := rooms.Messages(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || !messages[0].Wake || !strings.Contains(messages[0].Text, "child · idle for 1m") {
		t.Fatalf("check-in posts %+v", messages)
	}
	if decision, _ := room.RootContext(messages[0], rec.Name); decision != room.RootWake {
		t.Fatalf("root check-in is not input: %v", decision)
	}
	if sessions.Owes(parent, messages[0]) {
		t.Fatal("root check-in created a report obligation")
	}
	ordinary := parent
	ordinary.Role = ""
	if sessions.Owes(ordinary, messages[0]) {
		t.Fatal("ordinary parent check-in created a report obligation")
	}
	// A crash after Post but before the due-row update retries this exact ID,
	// even though the current summary would show a different elapsed time.
	now = now.Add(2 * time.Minute)
	done, err := s.Deliver(t.Context(), parentcheckin.Target{ID: parent.ID, Room: rec.ID}, messages[0].ID)
	if err != nil || !done {
		t.Fatalf("retry delivered=%v error=%v", done, err)
	}
	after, err := rooms.Messages(rec.ID)
	if err != nil || len(after) != 1 || after[0].Text != messages[0].Text {
		t.Fatalf("retry changed posts: %+v %v", after, err)
	}
}
