package ancestry_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/plan/ancestry"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store/storetest"
)

func TestEmptyAncestryCannotBeFilledByChangedHeader(t *testing.T) {
	for _, owned := range []bool{false, true} {
		name := "unowned"
		if owned {
			name = "owned"
		}
		t.Run(name, func(t *testing.T) {
			dataDir := storetest.DataDir(t)
			t.Cleanup(func() { database.Close(dataDir) })
			dir := filepath.Join(dataDir, "runs", "wf_empty-import")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "plan.json")
			if err := os.WriteFile(path, []byte("{\"version\":1,\"resumedFrom\":\"\"}"), 0600); err != nil {
				t.Fatal(err)
			}
			var stamp string
			if owned {
				if err := state.MigrateMirrors(dataDir); err != nil {
					t.Fatal(err)
				}
				db, err := database.Open(dataDir)
				if err != nil {
					t.Fatal(err)
				}
				if err := database.Migrate(db, "plan.ancestry", 1, func(conn *sql.Conn, _ int) error {
					_, err := conn.ExecContext(t.Context(), "CREATE TABLE plan_ancestry (plan TEXT PRIMARY KEY, resumed_from TEXT NOT NULL)")
					return err
				}); err != nil {
					t.Fatal(err)
				}
				// The previous release stamped empty headers without inserting a row.
				if _, err := state.For(db).ImportIfForeign(t.Context(), "plans/wf_empty-import", path, func(*sql.Conn, []byte) error { return nil }); err != nil {
					t.Fatal(err)
				}
				if err := db.QueryRow("SELECT version FROM state_mirrors WHERE key='plans/wf_empty-import'").Scan(&stamp); err != nil {
					t.Fatal(err)
				}
			}
			if err := ancestry.Migrate(dataDir); err != nil {
				t.Fatal(err)
			}
			if owned {
				db, err := database.Open(dataDir)
				if err != nil {
					t.Fatal(err)
				}
				var after string
				if err := db.QueryRow("SELECT version FROM state_mirrors WHERE key='plans/wf_empty-import'").Scan(&after); err != nil || after != stamp {
					t.Fatalf("migration changed ownership stamp: %q, %v", after, err)
				}
			}
			s := ancestry.Store{DataDir: dataDir}
			if parent, err := s.Parent(t.Context(), dir); err != nil || parent != "" {
				t.Fatalf("initial parent = %q, %v", parent, err)
			}
			if err := os.WriteFile(path, []byte("{\"version\":1,\"resumedFrom\":\"wf_stale\"}"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := database.Close(dataDir); err != nil {
				t.Fatal(err)
			}
			if err := ancestry.Migrate(dataDir); err != nil {
				t.Fatal(err)
			}
			if parent, err := s.Parent(t.Context(), dir); err != nil || parent != "" {
				t.Fatalf("changed header filled canonical empty parent: %q, %v", parent, err)
			}
		})
	}
}
