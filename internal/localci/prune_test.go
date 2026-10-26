package localci

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/database"
)

func TestPruneResultsDeletesOnlyOldResultsOutsideKeep(t *testing.T) {
	ctx := context.Background()
	s := fixtureStore(t)
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	t.Cleanup(func() { resultClock = time.Now })
	save := func(sha string, at time.Time) {
		t.Helper()
		resultClock = func() time.Time { return at }
		if err := s.SaveResult(ctx, Result{Repo: "github.com/acme/widgets", SHA: strings.Repeat(sha, 40), Status: OutcomePassed}); err != nil {
			t.Fatal(err)
		}
	}
	old, recent := now.Add(-15*24*time.Hour), now.Add(-time.Hour)
	save("a", old)
	save("b", old) // live: an open PR head
	save("c", recent)
	save("a", old.Add(-time.Hour)) // saved again, still old

	cutoff := now.Add(-14 * 24 * time.Hour)
	n, err := s.PruneResults(ctx, cutoff, map[string]bool{strings.Repeat("b", 40): true})
	if err != nil || n != 1 {
		t.Fatalf("PruneResults removed %d, %v; want 1", n, err)
	}
	for sha, want := range map[string]bool{"a": false, "b": true, "c": true} {
		_, err := s.LoadResult(ctx, "github.com/acme/widgets", strings.Repeat(sha, 40))
		if (err == nil) != want {
			t.Fatalf("result %s present = %v, want %v (%v)", sha, err == nil, want, err)
		}
	}
}

// Rows that exist before the recorded_at column get the migration time, so a
// first prune cannot delete the whole history at once.
func TestMigrationStampsExistingResultsWithTheMigrationTime(t *testing.T) {
	s := Store{DataDir: t.TempDir()}
	t.Cleanup(func() { database.Close(s.DataDir) })
	db, err := database.Open(s.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	err = database.Migrate(db, "localci.schema", 4, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), storeSchema+`;INSERT INTO local_ci_results(repo,sha,payload) VALUES('r','s','{}')`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if db, err = s.open(); err != nil {
		t.Fatal(err)
	}
	var at int64
	if err := db.QueryRow("SELECT recorded_at FROM local_ci_results").Scan(&at); err != nil {
		t.Fatal(err)
	}
	if age := time.Since(time.Unix(at, 0)); age < 0 || age > time.Minute {
		t.Fatalf("recorded_at is %s old, want the migration time", age)
	}
}
