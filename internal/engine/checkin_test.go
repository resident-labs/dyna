package engine

import (
	"testing"
	"time"

	accountservice "dyna-agent/internal/account/service"
	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/automations"
	"dyna-agent/internal/database"
	"dyna-agent/internal/engine/stores"
	"dyna-agent/internal/goals"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

// The parent check-ins hook is the only owner of check-ins, so switching it off
// stops them everywhere, including the pass a turn end would trigger.
func TestDisabledCheckInHookStopsCheckIns(t *testing.T) {
	root := storetest.Isolate(t)
	if err := stores.Migrate(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(root) })
	ctx := t.Context()
	rooms := room.DefaultStore()
	rec, err := rooms.Create(room.CreateInput{Name: "dyna"})
	if err != nil {
		t.Fatal(err)
	}
	sessions := subagent.DefaultStore()
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	parent := subagent.Meta{ID: subagent.NewID(), Name: "root", Role: subagent.RoleRoot, RoomID: rec.ID, Room: rec.Name, Status: subagent.StatusIdle, Created: now}
	child := subagent.Meta{ID: subagent.NewID(), Name: "child", ParentSession: parent.ID, RoomID: rec.ID, Room: rec.Name, Status: subagent.StatusIdle, Created: now, LastActivity: now}
	recordtest.Write(t, sessions.Root, parent.ID, parent)
	recordtest.Write(t, sessions.Root, child.ID, child)

	n := notify.New(rooms, &subagent.Manager{Store: sessions})
	accounts := accountservice.New(accountservice.Options{Store: accountstore.DefaultStore()})
	service := hooks.New(automations.Hooks(n, accounts)...)
	if err := service.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	if minutes, err := checkInInterval(ctx); err != nil || minutes != 15*time.Minute {
		t.Fatalf("seeded interval = %s, %v", minutes, err)
	}
	due := func() {
		t.Helper()
		db, err := database.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("UPDATE parent_checkins SET due=?", time.Now().Add(-time.Minute).UnixNano()); err != nil {
			t.Fatal(err)
		}
	}
	posts := func() int {
		t.Helper()
		msgs, err := rooms.Messages(rec.ID)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, m := range msgs {
			if _, ok := m.Checkin(); ok {
				count++
			}
		}
		return count
	}

	// The first pass schedules the parent's first check-in; a due row then posts.
	if _, _, err := RunParentCheckIns(ctx, &roomsvc.Service{Notify: n}, nil); err != nil {
		t.Fatal(err)
	}
	due()
	if posted, _, err := RunParentCheckIns(ctx, &roomsvc.Service{Notify: n}, nil); err != nil || posted != 1 {
		t.Fatalf("enabled pass posted %d, %v", posted, err)
	}
	if got := posts(); got != 1 {
		t.Fatalf("check-in posts = %d", got)
	}

	// A switched-off hook runs no pass, even for a due check-in.
	off := true
	if err := service.Edit(ctx, "", string(automations.BuiltinParentCheckIns), "owner", hooks.Patch{Disabled: &off}); err != nil {
		t.Fatal(err)
	}
	if minutes, err := checkInInterval(ctx); err != nil || minutes != 0 {
		t.Fatalf("disabled interval = %s, %v", minutes, err)
	}
	due()
	if posted, _, err := RunParentCheckIns(ctx, &roomsvc.Service{Notify: n}, nil); err != nil || posted != 0 {
		t.Fatalf("disabled pass posted %d, %v", posted, err)
	}
	if got := posts(); got != 1 {
		t.Fatalf("a switched-off hook posted a check-in: %d", got)
	}
}

// A child that ends its turn with an open goal and nothing in flight wakes its
// parent at once, without waiting for the room interval.
func TestIdleChildWithOpenGoalWakesParentAtTurnEnd(t *testing.T) {
	root := storetest.Isolate(t)
	if err := stores.Migrate(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(root) })
	ctx := t.Context()
	rooms := room.DefaultStore()
	rec, err := rooms.Create(room.CreateInput{Name: "dyna"})
	if err != nil {
		t.Fatal(err)
	}
	sessions := subagent.DefaultStore()
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	parent := subagent.Meta{ID: subagent.NewID(), Name: "root", Role: subagent.RoleRoot, RoomID: rec.ID, Room: rec.Name, Status: subagent.StatusIdle, Created: now}
	child := subagent.Meta{ID: subagent.NewID(), Name: "child", ParentSession: parent.ID, RoomID: rec.ID, Room: rec.Name, Status: subagent.StatusIdle, Created: now, LastActivity: now}
	recordtest.Write(t, sessions.Root, parent.ID, parent)
	recordtest.Write(t, sessions.Root, child.ID, child)
	if _, err := goals.DefaultStore().Add(rec.ID, parent.ID, goals.Input{Text: "Ship the fix", Holders: []string{child.ID}}); err != nil {
		t.Fatal(err)
	}

	n := notify.New(rooms, &subagent.Manager{Store: sessions})
	accounts := accountservice.New(accountservice.Options{Store: accountstore.DefaultStore()})
	if err := hooks.New(automations.Hooks(n, accounts)...).Seed(ctx); err != nil {
		t.Fatal(err)
	}
	svc := &roomsvc.Service{Notify: n}
	// The first pass schedules the parent's check-in one interval out.
	if _, _, err := RunParentCheckIns(ctx, svc, nil); err != nil {
		t.Fatal(err)
	}
	posted, _, err := RunParentCheckIns(ctx, svc, []string{child.ID})
	if err != nil || posted != 1 {
		t.Fatalf("turn end posted %d check-ins, %v; want 1 at once", posted, err)
	}
	// The child idles again at once. The parent already had a check-in this interval.
	posted, _, err = RunParentCheckIns(ctx, svc, []string{child.ID})
	if err != nil || posted != 0 {
		t.Fatalf("second turn end in one interval posted %d check-ins, %v; want 0", posted, err)
	}
}
