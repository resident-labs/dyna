package shells

import (
	"path/filepath"
	"testing"
	"time"
)

func TestListOwnerSinceKeepsRunningAndRecentJobsOfOneOwner(t *testing.T) {
	s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	long, recent := now.Add(-5*time.Hour), now.Add(-10*time.Minute)
	save := func(id, owner string, ended *time.Time) {
		t.Helper()
		var ex *Exit
		if ended != nil {
			ex = &Exit{Ended: *ended}
		}
		if err := s.Save(Meta{ID: id, Cmd: []string{"true"}, Cwd: "/", Owner: owner, Started: long}, ex); err != nil {
			t.Fatal(err)
		}
	}
	save("sh_20261026-070000-00000001", "ann", &long)   // ended long ago
	save("sh_20261026-070000-00000002", "ann", &recent) // ended recently
	save("sh_20261026-070000-00000003", "bob", &recent) // another owner
	save("sh_20261026-070000-00000004", "ann", nil)     // running
	list, err := s.ListOwnerSince("ann", now.Add(-time.Hour))
	if err != nil || len(list) != 2 {
		t.Fatalf("ListOwnerSince = %d jobs, %v; want ann's running and recent ones", len(list), err)
	}
	for _, in := range list {
		if in.Owner != "ann" || in.ID == "sh_20261026-070000-00000001" {
			t.Fatalf("unexpected job %s of %s", in.ID, in.Owner)
		}
	}
}
