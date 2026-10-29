package roomsvc

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/store"
)

func TestQueueRowsWakeHookWorker(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	svc := new(Service)
	version := ""
	var events []Event
	emit := func(ev Event) { events = append(events, ev) }
	svc.scanHookSpool(&version, true, emit)
	// The scanner observes a queue row, whatever process wrote it.
	// Enqueue and durable event contents are covered by the hooks tests.
	db, err := database.Open(store.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Immediate(context.Background(), db, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(context.Background(), "INSERT INTO hook_queue(body) VALUES(?)", `{"event":"after-land"}`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	svc.scanHookSpool(&version, false, emit)
	if len(events) != 1 || events[0].Kind != "hooks" || !events[0].WorkerWake {
		t.Fatalf("events: %#v", events)
	}
	svc.scanHookSpool(&version, false, emit)
	if len(events) != 1 {
		t.Fatal("unchanged queue woke worker")
	}
	version = ""
	svc.scanHookSpool(&version, true, emit)
	if len(events) != 1 {
		t.Fatal("initial scan emitted event")
	}
	// A legacy spool file a previous-release emitter appended wakes the
	// worker the same way, with no queue row behind it.
	spool := filepath.Join(store.DataDir(), "hook-spool")
	if err := os.MkdirAll(spool, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spool, "1.json"), []byte(`{"event":"after-land"}`), 0600); err != nil {
		t.Fatal(err)
	}
	svc.scanHookSpool(&version, false, emit)
	if len(events) != 2 || events[1].Kind != "hooks" || !events[1].WorkerWake {
		t.Fatalf("legacy spool file did not wake worker: %#v", events)
	}
	svc.scanHookSpool(&version, false, emit)
	if len(events) != 2 {
		t.Fatal("unchanged spool woke worker")
	}
}
