package roomsvc

import (
	"dyna-agent/internal/lanesync"
	"testing"
)

func TestSyncOperationChangeWakesWorker(t *testing.T) {
	e := newEnv(t)
	version := ""
	var events []Event
	emit := func(ev Event) { events = append(events, ev) }
	e.svc.scanSync(&version, true, emit)
	if err := lanesync.New().Update(func(st *lanesync.Snapshot) error {
		st.Operations["repo"] = lanesync.Operation{Progress: lanesync.Progress{State: "building"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.svc.scanSync(&version, false, emit)
	e.svc.scanSync(&version, false, emit)
	if len(events) != 1 || events[0].Kind != "sync" || !events[0].WorkerWake {
		t.Fatalf("sync events=%+v", events)
	}
}
