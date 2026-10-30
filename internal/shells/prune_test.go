package shells

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/database"
)

func TestPruneIndexUpgrade(t *testing.T) {
	s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
	db, err := s.db()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX shell_jobs_prune; ALTER TABLE shell_jobs DROP COLUMN activity_output; ALTER TABLE shell_jobs DROP COLUMN activity_lease; ALTER TABLE shell_jobs DROP COLUMN activity_copier; ALTER TABLE shell_jobs DROP COLUMN activity_cpu; ALTER TABLE shell_jobs DROP COLUMN activity_since; UPDATE meta SET value='5' WHERE key='shells.version'`); err != nil {
		t.Fatal(err)
	}
	database.Close(filepath.Dir(s.Root))
	db, err = s.db()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(filepath.Dir(s.Root)) })
	rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT id FROM shell_jobs WHERE ended IS NOT NULL AND ended<? AND keep=0 LIMIT ?`, "2026-10-25T00:00:00Z", 500)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		found = found || strings.Contains(detail, "USING INDEX shell_jobs_prune")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("prune query does not use the index after upgrade")
	}
}

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

	for i := 1; i <= 6; i++ {
		id := fmt.Sprintf("sh_20261022-120000-%08d", i)
		if err := os.MkdirAll(filepath.Join(s.Root, id), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(s.Root, id, "output.log"), []byte("output"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cutoff := now.Add(-3 * 24 * time.Hour)
	if n, err := s.PruneEnded(context.Background(), cutoff, 2); err != nil || n != 2 {
		t.Fatalf("first batch removed %d, %v; want 2", n, err)
	}
	if n, err := s.PruneEnded(context.Background(), cutoff, 2); err != nil || n != 1 {
		t.Fatalf("second batch removed %d, %v; want 1", n, err)
	}
	for i := 1; i <= 6; i++ {
		id := fmt.Sprintf("sh_20261022-120000-%08d", i)
		_, err := os.Stat(filepath.Join(s.Root, id))
		if i <= 3 && !os.IsNotExist(err) || i > 3 && err != nil {
			t.Fatalf("directory %s: %v", id, err)
		}
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
