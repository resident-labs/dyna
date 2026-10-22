package subagent

import (
	"context"
	"database/sql"
	"path/filepath"

	"dyna-agent/internal/database"
	"encoding/json"
	"sync"
	"testing"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/sessionlife"
)

// A dormant close's mark can expire while the close checks the session. A
// wake that takes no session lock must then win: the close's final write
// sees the woken record and backs off. (Goals take the session lock, so this
// guard is a second line.)
func TestDormantCloseKeepsASessionWokenAfterItsMarkExpired(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	info, _ := e.start("SLEEP:1 RESPOND: idle")
	info, _ = e.wait(info.ID, 1)
	if _, err := e.finish(info.ID); err != nil {
		t.Fatal(err)
	}
	sessions := e.m.Store.root()
	var woke error
	e.m.checkpoint = func(stage string) {
		if stage != "dormant-close-write" {
			return
		}
		// Expire the mark: its holder process is gone.
		var rec map[string]any
		if err := sessionlife.Load(sessions, info.ID, &rec); err != nil {
			t.Error(err)
			return
		}
		rec["closing"].(map[string]any)["pid"] = 2147483646
		if err := sessionlife.Save(sessions, info.ID, rec); err != nil {
			t.Error(err)
			return
		}
		// A wake that takes no session lock (ActivateTx on its own transaction).
		db, err := database.Open(filepath.Dir(sessions))
		if err != nil {
			woke = err
			return
		}
		woke = database.Immediate(context.Background(), db, func(conn *sql.Conn) error { return sessionlife.ActivateTx(conn, sessions, info.ID) })
	}
	ownerDismissEnv(t)
	_, err := e.m.DismissSession(e.ctx, info.ID)
	e.m.checkpoint = nil
	if woke != nil {
		t.Fatalf("waking the dormant session: %v", woke)
	}
	if err == nil {
		t.Fatal("the dormant close finished after the session was woken")
	}
	var got struct {
		Status string
		Closed json.RawMessage
	}
	if err := sessionlife.Load(sessions, info.ID, &got); err != nil || got.Status == StatusClosed {
		t.Fatalf("woken session = %+v, %v; want it open", got, err)
	}
}

// A session's metadata update reads, then writes under the session lock. A
// goal that wakes the session must wait for that lock, or the update's write
// would put back the dormant state it read.
func TestGoalWakeIsNotLostToAMetadataUpdate(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	info, _ := e.start("SLEEP:1 RESPOND: idle")
	info, _ = e.wait(info.ID, 1)
	if _, err := e.finish(info.ID); err != nil {
		t.Fatal(err)
	}
	added := make(chan error, 1)
	reached := make(chan struct{})
	var once sync.Once
	defer sessionlife.SetBeforeSessionLock(func(id string) {
		if id == info.ID {
			once.Do(func() { close(reached) })
		}
	})()
	if _, err := e.m.Store.UpdateMeta(info.ID, func(m *Meta) {
		go func() {
			_, err := e.m.goalStore().Add(info.RoomID, "owner", goals.Input{Text: "More work", Holders: []string{info.ID}})
			added <- err
		}()
		// The goal must stop at this session's lock, which the update holds.
		// A goal that finishes first woke the session without the lock.
		select {
		case <-reached:
		case err := <-added:
			t.Errorf("the goal woke the session during its metadata update: %v", err)
			added <- err
		}
		m.Name = "renamed"
	}); err != nil {
		t.Fatal(err)
	}
	if err := <-added; err != nil {
		t.Fatal(err)
	}
	got, err := e.m.Store.ReadMeta(info.ID)
	if err != nil || got.Status != StatusIdle || !got.DormantWake || got.Name != "renamed" {
		t.Fatalf("holder after the update and the goal = %+v, %v; want it awake and renamed", got, err)
	}
}
