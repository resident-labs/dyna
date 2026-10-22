package state

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
)

func reopenRequestWriter(t *testing.T, w *Writer) *Writer {
	t.Helper()
	var file string
	if err := w.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&file); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(file)
	if err := database.Close(dir); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if db == w.db {
		t.Fatal("database reopen reused the old connection")
	}
	return newWriter(db)
}

func TestRequestFingerprintSurvivesFirstCommitReopen(t *testing.T) {
	w, _ := requestWriter(t)
	fingerprint := []byte("original-fingerprint")
	fresh := func(method string, digest []byte) context.Context {
		return ForRequestCall(t.Context(), "admission-1", method, digest)
	}
	if err := w.Write(fresh("Admit", fingerprint), hub.Event{}, insertThing); err != nil {
		t.Fatal(err)
	}
	// No finish call occurs before the simulated engine restart.
	w = reopenRequestWriter(t, w)
	if result, done, err := w.RequestDone(fresh("Admit", fingerprint), "admission-1"); err != nil || !done || result != nil {
		t.Fatalf("matching crash replay: %q %v %v", result, done, err)
	}
	for _, tc := range []struct {
		name, method string
		digest       []byte
	}{
		{"method", "Other", fingerprint},
		{"arguments", "Admit", []byte("changed-fingerprint")},
		{"missing metadata", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := fresh(tc.method, tc.digest)
			if result, _, err := w.RequestDone(ctx, "admission-1"); !errors.Is(err, ErrRequestConflict) || result != nil {
				t.Errorf("changed crash replay: %q %v", result, err)
			}
			called := false
			err := w.Write(fresh(tc.method, tc.digest), hub.Event{}, func(conn *sql.Conn) error { called = true; return insertThing(conn) })
			if !errors.Is(err, ErrRequestConflict) || called {
				t.Errorf("first-receipt comparison: %v, callback %v", err, called)
			}
		})
	}
	called := false
	if err := w.Write(fresh("Admit", fingerprint), hub.Event{}, func(*sql.Conn) error { called = true; return nil }); !errors.Is(err, ErrRepeated) || called {
		t.Fatalf("matching receipt replay: %v, callback %v", err, called)
	}
	if got := countThings(t, w); got != 1 {
		t.Fatalf("replay changed mutation count: %d", got)
	}
}

func TestRequestFinishPathsPreserveFingerprint(t *testing.T) {
	for _, inTransaction := range []bool{false, true} {
		name := "FinishRequest"
		if inTransaction {
			name = "FinishRequestIn"
		}
		t.Run(name, func(t *testing.T) {
			w, _ := requestWriter(t)
			original := []byte("first")
			fresh := func(method string, digest []byte) context.Context {
				return ForRequestCall(t.Context(), "finish-1", method, digest)
			}
			if err := w.Write(fresh("Admit", original), hub.Event{}, insertThing); err != nil {
				t.Fatal(err)
			}
			finish := func(ctx context.Context, method string, result []byte) error {
				if inTransaction {
					return w.Write(t.Context(), hub.Event{}, func(conn *sql.Conn) error { return w.FinishRequestIn(ctx, conn, method, result) })
				}
				return w.FinishRequest(ctx, "finish-1", method, result)
			}
			if err := finish(fresh("Admit", []byte("changed")), "Admit", []byte("wrong")); !errors.Is(err, ErrRequestConflict) {
				t.Errorf("changed finish: %v", err)
			}
			if err := finish(fresh("Other", original), "Other", []byte("wrong")); !errors.Is(err, ErrRequestConflict) {
				t.Errorf("changed method finish: %v", err)
			}
			if !inTransaction {
				if err := finish(t.Context(), "Admit", []byte("wrong")); !errors.Is(err, ErrRequestConflict) {
					t.Errorf("metadata-free finish erased protection: %v", err)
				}
			}
			want := []byte("true")
			if err := finish(fresh("Admit", original), "Admit", want); err != nil {
				t.Fatal(err)
			}
			var saved []byte
			if err := w.db.QueryRow("SELECT fingerprint FROM request_ids WHERE id='finish-1'").Scan(&saved); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(saved, original) {
				t.Fatalf("finish replaced fingerprint: %q", saved)
			}
			if result, done, err := w.RequestDone(fresh("Admit", original), "finish-1"); err != nil || !done || !bytes.Equal(result, want) {
				t.Fatalf("finished replay: %q %v %v", result, done, err)
			}
		})
	}
}

func TestRequestFingerprintMigrationRejectsUnboundReplay(t *testing.T) {
	for _, tc := range []struct {
		name        string
		version     int
		fingerprint []byte
	}{
		{"v1", 1, nil}, {"v2-null", 2, nil}, {"v2-empty", 2, []byte{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Cleanup(func() { database.Close(dir) })
			db, err := database.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			first := time.Unix(1_800_000_000, 0)
			if err := database.Migrate(db, "state.requests", tc.version, func(conn *sql.Conn, _ int) error {
				schema := "CREATE TABLE request_ids(id TEXT PRIMARY KEY,method TEXT NOT NULL DEFAULT '',result BLOB,done_at INTEGER NOT NULL); CREATE INDEX request_ids_done ON request_ids(done_at)"
				if tc.version == 2 {
					schema = requestSchema
				}
				if _, err := conn.ExecContext(t.Context(), schema); err != nil {
					return err
				}
				if tc.version == 1 {
					_, err := conn.ExecContext(t.Context(), "INSERT INTO request_ids(id,method,result,done_at) VALUES('old','Thing',?,?)", []byte("true"), first.UnixMilli())
					return err
				}
				_, err := conn.ExecContext(t.Context(), "INSERT INTO request_ids(id,method,result,fingerprint,done_at) VALUES('old','Thing',?,?,?)", []byte("true"), tc.fingerprint, first.UnixMilli())
				return err
			}); err != nil {
				t.Fatal(err)
			}
			for pass := 0; pass < 2; pass++ {
				if err := MigrateRequests(dir); err != nil {
					t.Fatal(err)
				}
				if err := database.Close(dir); err != nil {
					t.Fatal(err)
				}
				db, err = database.Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				w := newWriter(db)
				w.Now = func() time.Time { return first.Add(RequestKeep) }
				call := ForRequestCall(t.Context(), "old", "Thing", []byte("fresh-fingerprint"))
				if _, _, err := w.RequestDone(call, "old"); !errors.Is(err, ErrRequestConflict) {
					t.Errorf("bound replay accepted: %v", err)
				}
				ran := false
				if err := w.Write(call, hub.Event{}, func(*sql.Conn) error { ran = true; return nil }); !errors.Is(err, ErrRequestConflict) || ran {
					t.Errorf("write replay: %v callback=%v", err, ran)
				}
				if err := w.FinishRequest(call, "old", "Thing", []byte("false")); !errors.Is(err, ErrRequestConflict) {
					t.Errorf("finish replay accepted: %v", err)
				}
				var method string
				var result, fingerprint []byte
				var doneAt int64
				if err := db.QueryRow("SELECT method,result,fingerprint,done_at FROM request_ids WHERE id='old'").Scan(&method, &result, &fingerprint, &doneAt); err != nil {
					t.Fatal(err)
				}
				if method != "Thing" || string(result) != "true" || string(fingerprint) != "unbound" || doneAt != first.UnixMilli() {
					t.Errorf("receipt evidence changed: %s %q %q %d", method, result, fingerprint, doneAt)
				}
				if pass == 1 {
					if n, err := w.PruneRequests(t.Context(), w.Now().Add(-RequestKeep)); err != nil || n != 0 {
						t.Fatalf("pruned at cutoff: %d %v", n, err)
					}
					w.Now = func() time.Time { return first.Add(RequestKeep + time.Millisecond) }
					if n, err := w.PruneRequests(t.Context(), w.Now().Add(-RequestKeep)); err != nil || n != 1 {
						t.Fatalf("retention: %d %v", n, err)
					}
				}
			}
		})
	}
}

// Calls with only file/log effects acquire their receipt at result finish.
func TestRequestFinishWithoutMutationProtectsFingerprint(t *testing.T) {
	w, _ := requestWriter(t)
	original := []byte("result-only")
	call := ForRequestCall(t.Context(), "result-only", "Echo", original)
	if err := w.FinishRequest(call, "result-only", "Echo", []byte("true")); err != nil {
		t.Fatal(err)
	}
	retry := ForRequestCall(t.Context(), "result-only", "Echo", original)
	if result, done, err := w.RequestDone(retry, "result-only"); err != nil || !done || string(result) != "true" {
		t.Fatalf("result-only replay: %q %v %v", result, done, err)
	}
	changed := ForRequestCall(t.Context(), "result-only", "Echo", []byte("different"))
	if result, _, err := w.RequestDone(changed, "result-only"); !errors.Is(err, ErrRequestConflict) || result != nil {
		t.Fatalf("changed result-only replay: %q %v", result, err)
	}
	if err := w.FinishRequest(changed, "result-only", "Echo", []byte("false")); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("changed result-only finish: %v", err)
	}
}
