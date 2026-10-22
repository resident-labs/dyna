package preferences

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store/storetest"
)

func TestPreferenceIndexImportsOnceAndDoesNotRestoreForgottenAccount(t *testing.T) {
	for _, canonical := range []bool{false, true} {
		t.Run(map[bool]string{false: "retained", true: "canonical"}[canonical], func(t *testing.T) {
			dataDir := storetest.DataDir(t)
			db, err := database.Open(dataDir)
			if err != nil {
				t.Fatal(err)
			}
			if err := database.Migrate(db, "harness.preferences", 1, func(conn *sql.Conn, _ int) error {
				_, err := conn.ExecContext(context.Background(), schema)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			metadata := filepath.Join(t.TempDir(), "harness-preferences.json")
			raw := []byte(`{"forgotten":{"settings":"retained","harness":"codex"}}`)
			if err := os.WriteFile(metadata, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if canonical {
				if _, err := db.Exec(`INSERT INTO harness_preferences(account,ref) VALUES('current','{"settings":"current","harness":"codex"}')`); err != nil {
					t.Fatal(err)
				}
			}
			if err := MigrateFrom(dataDir, metadata); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM harness_preferences WHERE account = 'forgotten'").Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 1
			if canonical {
				want = 0
			}
			if count != want {
				t.Fatalf("eager retained rows = %d, want %d", count, want)
			}
			if canonical {
				var current string
				if err := db.QueryRow("SELECT json_extract(ref,'$.settings') FROM harness_preferences WHERE account = 'current'").Scan(&current); err != nil || current != "current" {
					t.Fatalf("canonical ref = %q, %v", current, err)
				}
			}
			if err := state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error { return Forget(context.Background(), conn, "forgotten") }); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(metadata); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(metadata, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := MigrateFrom(dataDir, metadata); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := For("forgotten"); err != nil || ok {
				t.Fatalf("forgotten ref restored: %v, %v", ok, err)
			}
			if got, err := os.ReadFile(metadata); err != nil || string(got) != string(raw) {
				t.Fatalf("source changed: %q, %v", got, err)
			}
		})
	}
}
