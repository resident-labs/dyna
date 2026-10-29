package roomsvc

import (
	"context"
	"database/sql"
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
}
