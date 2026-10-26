package shells

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneEndedRemovesOnlyOldEndedUnkeptJobsInBatches(t *testing.T) {
	s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	old, recent := now.Add(-4*24*time.Hour), now.Add(-time.Hour)
	save := func(id string, keep bool, ended *time.Time) {
		t.Helper()
		m := Meta{ID: id, Cmd: []string{"true"}, Cwd: "/", Keep: keep, Started: old}
		var ex *Exit
		if ended != nil {
			ex = &Exit{Ended: *ended}
		}
		if err := s.Save(m, ex); err != nil {
			t.Fatal(err)
		}
	}
	save("sh_20261022-120000-00000001", false, &old)
	save("sh_20261022-120000-00000002", false, &old)
	save("sh_20261022-120000-00000003", false, &old)
	save("sh_20261022-120000-00000004", true, &old)     // kept
	save("sh_20261022-120000-00000005", false, &recent) // ended recently
	save("sh_20261022-120000-00000006", false, nil)     // still running

	cutoff := now.Add(-3 * 24 * time.Hour)
	if n, err := s.PruneEnded(context.Background(), cutoff, 2); err != nil || n != 2 {
		t.Fatalf("first batch removed %d, %v; want 2", n, err)
	}
	if n, err := s.PruneEnded(context.Background(), cutoff, 2); err != nil || n != 1 {
		t.Fatalf("second batch removed %d, %v; want 1", n, err)
	}
	list, err := s.List(true)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, info := range list {
		got[info.ID] = true
	}
	for _, want := range []string{"sh_20261022-120000-00000004", "sh_20261022-120000-00000005", "sh_20261022-120000-00000006"} {
		if !got[want] || len(got) != 3 {
			t.Fatalf("jobs left = %v; want the kept, recent and running ones", got)
		}
	}
}
