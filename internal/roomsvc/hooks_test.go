package roomsvc

import (
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/store"
)

func TestSpoolWritesWakeHookWorker(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	svc := new(Service)
	version := ""
	var events []Event
	emit := func(ev Event) { events = append(events, ev) }
	svc.scanHookSpool(&version, true, emit)
	// The scanner observes a spool write, independent of repository identity.
	// Enqueue and durable event contents are covered by the hooks tests.
	spool := filepath.Join(store.DataDir(), "hook-spool")
	if err := os.MkdirAll(spool, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spool, "00000000000000000001.json"), []byte(`{"event":"after-land"}`), 0600); err != nil {
		t.Fatal(err)
	}
	svc.scanHookSpool(&version, false, emit)
	if len(events) != 1 || events[0].Kind != "hooks" || !events[0].WorkerWake {
		t.Fatalf("events: %#v", events)
	}
	svc.scanHookSpool(&version, false, emit)
	if len(events) != 1 {
		t.Fatal("unchanged spool woke worker")
	}
	version = ""
	svc.scanHookSpool(&version, true, emit)
	if len(events) != 1 {
		t.Fatal("initial scan emitted event")
	}
}
