package devicegrant

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/scope"
)

func TestStartupImportsDeviceScopesOnce(t *testing.T) {
	for _, checkpoint := range []bool{false, true} {
		t.Run(map[bool]string{false: "import", true: "checkpoint"}[checkpoint], func(t *testing.T) {
			dir := t.TempDir()
			db, err := database.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { database.Close(dir) })
			if err := database.Migrate(db, "device_grants.version", 1, func(conn *sql.Conn, _ int) error {
				if _, err := conn.ExecContext(context.Background(), schema); err != nil {
					return err
				}
				_, err := conn.ExecContext(context.Background(), `INSERT INTO device_grants VALUES('paired','narrow','["read"]',0)`)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if checkpoint {
				if _, err := db.Exec("INSERT INTO meta VALUES('device_grants.legacy_imported','1')"); err != nil {
					t.Fatal(err)
				}
			}
			sources := map[string]string{
				"web/devices.json":      `[{"id":"narrow"},{"id":"old"}]`,
				"connect/sessions.json": `[{"id":"cs_old"}]`,
			}
			if checkpoint {
				sources["web/devices.json"] = "invalid JSON"
			}
			for path, body := range sources {
				path = filepath.Join(dir, path)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := Migrate(dir); err != nil {
				t.Fatal(err)
			}
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			narrow, ok, err := s.Get("paired", "narrow")
			if err != nil || !ok || narrow.Legacy || !reflect.DeepEqual(narrow.Scopes, []string{"read"}) {
				t.Fatalf("explicit grant changed: %+v %v", narrow, err)
			}
			for kind, id := range map[string]string{"paired": "old", "connect": "cs_old"} {
				g, ok, err := s.Get(kind, id)
				if err != nil || ok == checkpoint || (ok && (!g.Legacy || !reflect.DeepEqual(g.Scopes, scope.All()))) {
					t.Fatalf("startup grant %s: %+v %v %v", kind, g, ok, err)
				}
			}
			for path, want := range sources {
				body, err := os.ReadFile(filepath.Join(dir, path))
				if err != nil || string(body) != want {
					t.Fatalf("source changed: %s %v", path, err)
				}
				if err := os.WriteFile(filepath.Join(dir, path), []byte(`[{"id":"later"}]`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := database.Close(dir); err != nil {
				t.Fatal(err)
			}
			s, err = Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{"paired", "connect"} {
				if _, ok, err := s.Get(kind, "later"); err != nil || ok {
					t.Fatalf("later %s got implicit grant: %v %v", kind, ok, err)
				}
			}
		})
	}
}
