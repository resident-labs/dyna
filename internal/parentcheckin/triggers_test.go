package parentcheckin_test

import (
	"bytes"
	"testing"
	"time"

	"dyna-agent/internal/automations"
	"dyna-agent/internal/database"
	"dyna-agent/internal/engine"
	"dyna-agent/internal/engine/stores"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/parentcheckin"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

func TestAlternatingCheckInTriggersDoNotPostponeDueCheckIn(t *testing.T) {
	root := storetest.Isolate(t)
	if err := stores.Migrate(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(root) })
	ctx := t.Context()
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	restoreClock := parentcheckin.SetNowForTest(func() time.Time { return now })
	t.Cleanup(restoreClock)

	rooms := room.DefaultStore()
	rec, err := rooms.Create(room.CreateInput{Name: "dyna"})
	if err != nil {
		t.Fatal(err)
	}
	sessions := subagent.DefaultStore()
	parent := subagent.Meta{ID: subagent.NewID(), Name: "root", Role: subagent.RoleRoot, RoomID: rec.ID, Room: rec.Name, Status: subagent.StatusIdle, Created: now}
	child := subagent.Meta{ID: subagent.NewID(), Name: "child", ParentSession: parent.ID, RoomID: rec.ID, Room: rec.Name, Status: subagent.StatusIdle, Created: now, LastActivity: now}
	recordtest.Write(t, sessions.Root, parent.ID, parent)
	recordtest.Write(t, sessions.Root, child.ID, child)
	n := notify.New(rooms, &subagent.Manager{Store: sessions})

	specs := automations.Hooks(n, nil)
	if err := hooks.New(specs...).Seed(ctx); err != nil {
		t.Fatal(err)
	}
	var checkInHook hooks.BuiltinSpec
	for _, spec := range specs {
		if spec.Builtin == automations.BuiltinParentCheckIns {
			checkInHook = spec
		}
	}
	if checkInHook.Run == nil {
		t.Fatal("parent check-ins hook has no pass")
	}
	runHook := func() {
		t.Helper()
		var log bytes.Buffer
		if err := checkInHook.Run(ctx, checkInHook.EveryMinutes, &log); err != nil {
			t.Fatal(err)
		}
	}
	runTurnEnd := func() {
		t.Helper()
		if _, _, err := engine.RunParentCheckIns(ctx, &roomsvc.Service{Notify: n}, nil); err != nil {
			t.Fatal(err)
		}
	}
	posts := func() int {
		t.Helper()
		messages, err := rooms.Messages(rec.ID)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, message := range messages {
			if _, ok := message.Checkin(); ok {
				count++
			}
		}
		return count
	}

	runTurnEnd()
	now = now.Add(14 * time.Minute)
	runHook()
	runTurnEnd()
	now = now.Add(time.Minute)
	runHook()
	if got := posts(); got != 1 {
		t.Fatalf("alternating triggers posted %d check-ins at the due time, want 1", got)
	}
}
