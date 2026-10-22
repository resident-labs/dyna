package roomsvc

import (
	"testing"
	"time"

	"dyna-agent/internal/listpage"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/terminalsvc"
)

// Identify the actual gate that rebuilds pages. Healthy idle scans and
// duplicate row notifications must not rebuild the room heads.
func TestShellWatchCountsUnchangedRecoveryAndExpiryWork(t *testing.T) {
	e := newEnv(t)
	a, b := e.room("shell-count-a"), e.room("shell-count-b")
	store := terminalsvc.ShellStore(e.svc.sessions().Root)
	now := time.Now()
	clock := terminalsvc.Clock
	terminalsvc.Clock = func() time.Time { return now }
	t.Cleanup(func() { terminalsvc.Clock = clock })
	exit := &shells.Exit{Ended: now.Add(-time.Hour)}
	job, err := store.Create(shells.StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir(), RoomID: a.ID, Owner: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(job, exit); err != nil {
		t.Fatal(err)
	}
	other, err := store.Create(shells.StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir(), RoomID: b.ID, Owner: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(other, exit); err != nil {
		t.Fatal(err)
	}
	type work struct{ lists, records, rows, pages int }
	var counted work
	list, read, row, page := listShellJobs, readShellJobs, buildShellJobRow, pageShellJobs
	t.Cleanup(func() { listShellJobs, readShellJobs, buildShellJobRow, pageShellJobs = list, read, row, page })
	listShellJobs = func(s *shells.Store) ([]shells.Info, error) {
		counted.lists++
		return list(s)
	}
	readShellJobs = func(s *shells.Store, ids []string) ([]shells.Info, error) {
		counted.records += len(ids) // Infos returns before SQL for an empty set.
		return read(s, ids)
	}
	buildShellJobRow = func(s *shells.Store, in shells.Info) terminalsvc.ShellJob {
		counted.rows++
		return row(s, in)
	}
	pageShellJobs = func(items []listpage.Meta) (listpage.Page[listpage.Meta], error) {
		counted.pages++
		return page(items)
	}
	jobs := newShellJobWatch()
	t.Cleanup(jobs.close)
	check := func(label string, first bool, want work) {
		t.Helper()
		counted = work{}
		e.svc.scanShellJobs(jobs, first, func(Event) {})
		if counted != want {
			t.Fatalf("%s work = %+v; want %+v", label, counted, want)
		}
	}
	check("initial", true, work{lists: 1, rows: 2, pages: 2})
	check("unchanged", false, work{})
	store.Notify(job.ID)
	store.Notify(job.ID)
	check("duplicate notifications", false, work{records: 1, rows: 1})
	check("unchanged after notification", false, work{})
	jobs.close() // Recovery must subscribe before its full read.
	check("watch recovery", false, work{lists: 1, rows: 2, pages: 2})
	check("unchanged after recovery", false, work{})
	now = now.Add(24*time.Hour + time.Nanosecond)
	check("head expiry", false, work{pages: 2})
	check("unchanged after expiry", false, work{})
}
