package room

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

func TestPinMigrationPreservesCanonicalStateBeforeReadingStaleFiles(t *testing.T) {
	for _, canonical := range []string{"populated rows without revision", "empty set with revision"} {
		for _, source := range []string{"unstamped real source", "malformed stale source"} {
			t.Run(canonical+"/"+source, func(t *testing.T) {
				s, db, path, original, _ := pinsMigrationFixture(t)
				id := filepath.Base(filepath.Dir(path))
				var want []Pin
				wantRevision := int64(-1)
				if canonical == "populated rows without revision" {
					want = []Pin{{ID: "msg_current", By: "fixture:current", At: 42}}
				} else {
					wantRevision = 7
				}
				if err := state.For(db).Write(t.Context(), hub.Event{}, func(conn *sql.Conn) error {
					if len(want) > 0 {
						_, err := conn.ExecContext(t.Context(), "INSERT INTO room_pins(room_id,msg_id,pinned_by,at,seq) VALUES(?,?,?,?,?)", id, want[0].ID, want[0].By, want[0].At, 1)
						return err
					}
					_, err := conn.ExecContext(t.Context(), "INSERT INTO room_pin_revisions(room_id,revision) VALUES(?,?)", id, wantRevision)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				if source == "malformed stale source" {
					original = []byte("{")
					if err := os.WriteFile(path, original, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if err := Migrate(filepath.Dir(s.Root)); err != nil {
					t.Fatalf("canonical pin state required no source read: %v", err)
				}
				got, err := s.Pins(id)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("canonical pins = %+v, %v; want %+v", got, err, want)
				}
				var revision int64
				if err := db.QueryRow("SELECT COALESCE((SELECT revision FROM room_pin_revisions WHERE room_id=?),-1)", id).Scan(&revision); err != nil || revision != wantRevision {
					t.Fatalf("revision = %d, %v; want %d", revision, err, wantRevision)
				}
				if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, original) {
					t.Fatalf("stale source changed: %v", err)
				}
				if err := Migrate(filepath.Dir(s.Root)); err != nil {
					t.Fatal(err)
				}
				if after, err := s.Pins(id); err != nil || !reflect.DeepEqual(after, want) {
					t.Fatalf("replayed canonical pins = %+v, %v", after, err)
				}
			})
		}
	}
}
