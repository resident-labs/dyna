package state

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dyna-agent/internal/database"
)

func TestKeysChangedSinceUsesCoveringRevisionIndex(t *testing.T) {
	root := newRoot(t)
	dir := filepath.Dir(root)
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	// Start at the existing table version, before the additive index migration.
	if err := database.Migrate(db, "pr.state", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(t.Context(), schema)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.Immediate(t.Context(), db, func(conn *sql.Conn) error {
		for _, row := range []struct {
			key string
			rev int
		}{{"old", 0}, {"earlier", 1}, {"equal-a", 2}, {"equal-b", 2}, {"later", 7}, {"latest", 9}} {
			if _, err := conn.ExecContext(t.Context(), `INSERT INTO pr_records(key,room,repo,number,closed,rev,body)
VALUES(?,'room','owner/repo',0,0,?,?)`, row.key, row.rev, `{"padding":"`+strings.Repeat("x", 4096)+`"}`); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN SELECT key FROM pr_rows WHERE rev > ?", "2")
	if err != nil {
		t.Fatal(err)
	}
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	readErr, closeErr := rows.Err(), rows.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("query plan: read=%v, close=%v", readErr, closeErr)
	}
	if len(plan) != 1 || !strings.HasPrefix(plan[0], "SEARCH pr_rows USING COVERING INDEX pr_records_revision ") || !strings.Contains(plan[0], "(rev>?)") {
		t.Errorf("PR revision lookup is not a covering index search: %v", plan)
	}
	for _, tc := range []struct {
		since string
		want  []string
	}{
		{"0", []string{"earlier", "equal-a", "equal-b", "later", "latest"}},
		{"2", []string{"later", "latest"}},
		{"9", nil},
	} {
		got, err := KeysChangedSince(root, tc.since)
		slices.Sort(got)
		slices.Sort(tc.want)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("keys after %s: got %v, error %v; want %v", tc.since, got, err, tc.want)
		}
	}
	// A fresh handle accepts the current table version, and the index migration does not change it.
	if err := database.Close(dir); err != nil {
		t.Fatal(err)
	}
	if db, err = database.Open(dir); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db, "pr.state", 2, func(*sql.Conn, int) error {
		t.Fatal("the index migration changed the PR table version")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var version string
	if err := db.QueryRowContext(context.Background(), "SELECT value FROM meta WHERE key='pr.state'").Scan(&version); err != nil || version != "2" {
		t.Fatalf("PR table version: %q, %v; want 2", version, err)
	}
}
