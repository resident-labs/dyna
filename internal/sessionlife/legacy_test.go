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

// Records written before dyna.db are read from session.json until the
// import copies them. The import skips and reports damaged files, never
// overwrites a record written meanwhile, and never removes a file.
func TestImportLegacyCopiesOldRecordsOnce(t *testing.T) {
	sessions := filepath.Join(t.TempDir(), "sessions")
	write := func(id, body string) {
		if err := os.MkdirAll(filepath.Join(sessions, id), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sessions, id, "session.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("ses_old", `{"id":"ses_old","roomId":"r","room":"work","status":"idle"}`)
	write("ses_newer", `{"id":"ses_newer","status":"dormant"}`)
	write("ses_broken", `{`)
	var s struct{ Status string }
	if err := Load(sessions, "ses_old", &s); err != nil || s.Status != "idle" {
		t.Fatalf("legacy read before import = %+v, %v", s, err)
	}
	if err := Save(sessions, "ses_newer", map[string]any{"id": "ses_newer", "status": "idle"}); err != nil {
		t.Fatal(err)
	}
	n, damaged := ImportLegacy(sessions)
	if n != 1 || damaged == nil || !strings.Contains(damaged.Error(), "ses_broken") {
		t.Fatalf("ImportLegacy = %d, %v; want 1 imported and ses_broken reported", n, damaged)
	}
	if err := Load(sessions, "ses_newer", &s); err != nil || s.Status != "idle" {
		t.Fatalf("import replaced a newer record: %+v, %v", s, err)
	}
	if v, ok := RecordVersion(sessions, "ses_old"); !ok || !strings.HasPrefix(string(v), "r") {
		t.Fatalf("ses_old version = %q, %v; want a row revision", v, ok)
	}
	for _, id := range []string{"ses_old", "ses_newer", "ses_broken"} {
		if _, err := os.Stat(filepath.Join(sessions, id, "session.json")); err != nil {
			t.Fatalf("import removed %s/session.json: %v", id, err)
		}
	}
	if n, _ := ImportLegacy(sessions); n != 0 {
		t.Fatalf("second import copied %d records", n)
	}
	if err := Load(sessions, "ses_broken", &s); !errors.Is(err, ErrRecord) {
		t.Fatalf("damaged legacy record = %v, want ErrRecord", err)
	}
}

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
			if err == nil {
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

// Each reads the rows with one query and falls back to the legacy file only
// for folders that have no row. With open, closed rows are left out and
// their folders are not read as legacy records.
func TestEachReadsRowsThenLegacyFiles(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(sessions, "ses_broken", "session.json"), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	scan := func(open bool) (string, error) {
		var seen []string
		err := Each(sessions, open, func(id string, raw []byte, err error) error {
			if err == nil && !json.Valid(raw) {
				err = errors.New("invalid JSON")
			}
			if err != nil {
				seen = append(seen, id+"!")
			} else {
				seen = append(seen, id)
			}
			return nil
		})
		return strings.Join(seen, ","), err
	}
	if got, err := scan(true); err != nil || got != "ses_broken!,ses_legacy,ses_open" {
		t.Fatalf("open scan = %q, %v", got, err)
	}
	if got, err := scan(false); err != nil || got != "ses_broken!,ses_closed,ses_legacy,ses_open" {
		t.Fatalf("full scan = %q, %v", got, err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(sessions, "ses_link")); err != nil {
		t.Fatal(err)
	}
	if got, err := scan(false); !errors.Is(err, ErrRecord) || got != "ses_broken!,ses_closed,ses_legacy,ses_open" {
		t.Fatalf("scan with a symlinked entry = %q, %v; want the records and ErrRecord", got, err)
	}
}
