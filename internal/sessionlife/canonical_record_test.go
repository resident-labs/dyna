package sessionlife

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/database"
)

func TestCanonicalPointReadsUseOnlyValidStoredRows(t *testing.T) {
	sessions := filepath.Join(t.TempDir(), "sessions")
	id := "ses_20261023-000000-abcdef12"
	if err := os.MkdirAll(filepath.Join(sessions, id), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessions, id, legacyName), []byte(`{"id":"`+id+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	assertMissing := func() {
		t.Helper()
		if _, _, err := LoadRaw(sessions, id); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("LoadRaw missing = %v", err)
		}
		if version, ok := RecordVersion(sessions, id); ok || version != "" {
			t.Errorf("RecordVersion missing = %q, %v", version, ok)
		}
		if err := Check(sessions, id); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Check missing = %v", err)
		}
	}
	assertMissing()
	db, err := records(sessions)
	if err != nil {
		t.Fatal(err)
	}
	assertMissing()
	checkTransaction := func(want error) {
		t.Helper()
		err := database.Immediate(context.Background(), db, func(conn *sql.Conn) error {
			if err := CheckTx(conn, sessions, id); !errors.Is(err, want) {
				t.Errorf("CheckTx = %v, want %v", err, want)
			}
			if err := ActivateTx(conn, sessions, id); !errors.Is(err, want) {
				t.Errorf("ActivateTx = %v, want %v", err, want)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	checkTransaction(os.ErrNotExist)
	if err := Save(sessions, id, map[string]any{"id": id}); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"null", "[]", "{", "{} trailing", "{" + strings.Repeat(" ", recordLimit) + "}"} {
		if _, err := db.Exec("UPDATE session_records SET meta=? WHERE id=?", raw, id); err != nil {
			t.Fatal(err)
		}
		if _, _, err := LoadRaw(sessions, id); !errors.Is(err, ErrRecord) {
			t.Errorf("LoadRaw invalid metadata (length %d) = %v", len(raw), err)
		}
		checkTransaction(ErrRecord)
		var value any
		if err := LoadStored(context.Background(), db, id, &value); !errors.Is(err, ErrRecord) {
			t.Errorf("LoadStored invalid metadata (length %d) = %v", len(raw), err)
		}
		if err := Check(sessions, id); !errors.Is(err, ErrRecord) {
			t.Errorf("Check invalid metadata (length %d) = %v", len(raw), err)
		}
	}
}

// writeRawRecord seeds canonical metadata, including damaged test records.
func writeRawRecord(t *testing.T, sessions, id string, raw []byte) {
	t.Helper()
	if err := Save(sessions, id, map[string]any{"id": id}); err != nil {
		t.Fatal(err)
	}
	db, err := records(sessions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE session_records SET meta=? WHERE id=?", string(raw), id); err != nil {
		t.Fatal(err)
	}
}
