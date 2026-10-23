package sessionlife

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/database"
)

// Every write gets a new version, and a deleted record is gone even when
// its session folder stays.
func TestSaveVersionsAndDelete(t *testing.T) {
	sessions := filepath.Join(t.TempDir(), "sessions")
	id := "ses_20261020-000000-abcdef12"
	if err := os.MkdirAll(filepath.Join(sessions, id), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Save(sessions, id, map[string]any{"id": id}); err != nil {
		t.Fatal(err)
	}
	first, _ := RecordVersion(sessions, id)
	if err := Save(sessions, id, map[string]any{"id": id}); err != nil {
		t.Fatal(err)
	}
	if second, _ := RecordVersion(sessions, id); second == first || second == "" {
		t.Fatalf("versions %q then %q; want two different versions", first, second)
	}
	if err := Delete(sessions, id); err != nil {
		t.Fatal(err)
	}
	var s Snapshot
	if err := Load(sessions, id, &s); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load after Delete = %v, want os.ErrNotExist", err)
	}
}

// Reads never create the record table, so a read inside another store's
// transaction (which holds the writer) does not wait, even on a store that
// has never written a record.
func TestReadsInsideAnotherTransactionNeverWait(t *testing.T) {
	data := t.TempDir()
	sessions := filepath.Join(data, "sessions")
	if err := os.MkdirAll(filepath.Join(sessions, "ses_a"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessions, "ses_a", "session.json"), []byte(`{"id":"ses_a"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	err = database.Immediate(context.Background(), db, func(*sql.Conn) error {
		go func() {
			var s Snapshot
			err := Load(sessions, "ses_a", &s)
			if errors.Is(err, os.ErrNotExist) {
				_, err = IDs(sessions)
			}
			if err == nil {
				err = Check(sessions, "ses_a")
			}
			done <- err
		}()
		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			return errors.New("a record read waited for the writer")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Each reports damaged canonical rows through the callback and ignores
// retained files. Callbacks can write after the listing cursor closes.
func TestEachReadsCanonicalRows(t *testing.T) {
	sessions := filepath.Join(t.TempDir(), "sessions")
	mkdir := func(id string) {
		if err := os.MkdirAll(filepath.Join(sessions, id), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"ses_open", "ses_closed", "ses_legacy", "ses_broken", "ses_empty"} {
		mkdir(id)
	}
	if err := Save(sessions, "ses_open", map[string]any{"id": "ses_open"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(sessions, "ses_closed", map[string]any{"id": "ses_closed", "closed": "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	// A closed row whose folder still holds an old file: the row wins.
	if err := os.WriteFile(filepath.Join(sessions, "ses_closed", "session.json"), []byte(`{"id":"ses_closed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessions, "ses_legacy", "session.json"), []byte(`{"id":"ses_legacy"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	writeRawRecord(t, sessions, "ses_broken", []byte(`{`))
	scan := func(open bool) (string, error) {
		var seen []string
		err := Each(sessions, open, func(id string, raw []byte, err error) error {
			if err == nil && !json.Valid(raw) {
				err = errors.New("invalid JSON")
			}
			if err != nil {
				if !errors.Is(err, ErrRecord) {
					t.Errorf("damaged row %s: %v", id, err)
				}
				seen = append(seen, id+"!")
			} else {
				seen = append(seen, id)
			}
			if id == "ses_open" {
				return Save(sessions, id, map[string]any{"id": id})
			}
			return nil
		})
		return strings.Join(seen, ","), err
	}
	if got, err := scan(true); err != nil || got != "ses_broken!,ses_open" {
		t.Fatalf("open scan = %q, %v", got, err)
	}
	if got, err := scan(false); err != nil || got != "ses_broken!,ses_closed,ses_open" {
		t.Fatalf("full scan = %q, %v", got, err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(sessions, "ses_link")); err != nil {
		t.Fatal(err)
	}
	if got, err := scan(false); err != nil || got != "ses_broken!,ses_closed,ses_open" {
		t.Fatalf("scan with a symlinked entry = %q, %v; want only canonical records", got, err)
	}
}
