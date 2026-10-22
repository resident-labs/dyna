package roomsvc

import (
	"fmt"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"dyna-agent/internal/listpage"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/terminalsvc"
)

func TestShellHeadExpiryDoesNotCopyRetainedDisplayRows(t *testing.T) {
	e := newEnv(t)
	rec := e.room("shell-head-work")
	store := terminalsvc.ShellStore(e.svc.sessions().Root)
	now := time.Now()
	clock := terminalsvc.Clock
	terminalsvc.Clock = func() time.Time { return now }
	t.Cleanup(func() { terminalsvc.Clock = clock })
	job, err := store.Create(shells.StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir(), RoomID: rec.ID, Owner: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(job, &shells.Exit{Ended: now.Add(-listpage.Recent + time.Second)}); err != nil {
		t.Fatal(err)
	}
	jobs := newShellJobWatch()
	t.Cleanup(jobs.close)
	e.svc.scanShellJobs(jobs, true, func(Event) {})
	if !jobs.heads[rec.ID][job.ID] {
		t.Fatal("initial first page did not contain the expiring job")
	}
	// Seed the watch's retained history after initial synchronization. The
	// measured scan must select a page, not read thousands of store rows.
	const retained = 4000
	old := now.Add(-2 * listpage.Recent)
	for i := range retained {
		id := fmt.Sprintf("sh_retained_%04d", i)
		jobs.jobs[id] = shellJobRow{
			info: shells.Info{Meta: shells.Meta{ID: id, RoomID: rec.ID}, Status: "exited", Exit: &shells.Exit{Ended: old}},
			row:  terminalsvc.ShellJob{ID: id, Command: []string{"true"}, State: "exited", Started: old, Ended: &old},
		}
	}
	now = now.Add(2 * time.Second)
	var events []Event
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	e.svc.scanShellJobs(jobs, false, func(event Event) { events = append(events, event) })
	runtime.ReadMemStats(&after)
	if len(events) != 1 || events[0].Change == nil || events[0].Change.ID != job.ID || events[0].Change.InHead == nil || *events[0].Change.InHead {
		t.Fatalf("expiry event = %+v; want the real job to leave the first page", events)
	}
	// Metadata selection must use less space than one retained display-row
	// copy per job. Growing a slice of full ShellJob values exceeds this.
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated >= retained*uint64(unsafe.Sizeof(terminalsvc.ShellJob{})) {
		t.Fatalf("shell first-page expiry allocated %d bytes for %d retained display rows", allocated, retained)
	}
	e.svc.scanShellJobs(jobs, false, func(event Event) { t.Fatalf("unchanged scan repeated %+v", event) })
}
