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

func requestWriter(t *testing.T) (*Writer, *[]hub.Event) {
	t.Helper()
	w, published := testWriter(t)
	if _, err := w.db.Exec(requestSchema); err != nil {
		t.Fatal(err)
	}
	return w, published
}

func countThings(t *testing.T, w *Writer) int {
	t.Helper()
	var n int
	if err := w.db.QueryRow("SELECT COUNT(*) FROM things").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A write retried with the same request ID applies once and publishes once
// (requirement 9). Later writes of the same call still apply.
func TestRequestWriteAppliesOnce(t *testing.T) {
	w, published := requestWriter(t)
	ev := hub.Event{Topic: "things"}
	call := ForRequestCall(context.Background(), "r1", "Thing", []byte("thing"))
	if err := w.Write(call, ev, insertThing); err != nil {
		t.Fatal(err)
	}
	if err := w.Write(call, ev, insertThing); err != nil {
		t.Fatalf("second write of the same call: %v", err)
	}
	retry := ForRequestCall(context.Background(), "r1", "Thing", []byte("thing"))
	if err := w.Write(retry, ev, insertThing); !errors.Is(err, ErrRepeated) {
		t.Fatalf("retry = %v, want ErrRepeated", err)
	}
	if n := countThings(t, w); n != 2 || len(*published) != 2 {
		t.Fatalf("%d rows and %d events, want 2 and 2 (the retry adds none)", n, len(*published))
	}
	if result, done, err := w.RequestDone(context.Background(), "r1"); err != nil || !done || result != nil {
		t.Fatalf("RequestDone = %q, %v, %v; want done with no result yet", result, done, err)
	}
	if err := w.FinishRequest(call, "r1", "Thing", []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	if result, done, err := w.RequestDone(context.Background(), "r1"); err != nil || !done || string(result) != `{"ok":true}` {
		t.Fatalf("RequestDone = %q, %v, %v; want the stored result", result, done, err)
	}
}

// A write that fails records nothing, so its retry applies.
func TestFailedRequestWriteCanBeRetried(t *testing.T) {
	w, _ := requestWriter(t)
	fail := errors.New("no")
	if err := w.Write(ForRequest(context.Background(), "r2"), hub.Event{}, func(*sql.Conn) error { return fail }); !errors.Is(err, fail) {
		t.Fatalf("failed write = %v", err)
	}
	if err := w.Write(ForRequest(context.Background(), "r2"), hub.Event{}, insertThing); err != nil {
		t.Fatalf("retry after a failed write: %v", err)
	}
	if n := countThings(t, w); n != 1 {
		t.Fatalf("%d rows, want 1", n)
	}
}

// Request IDs older than RequestKeep are pruned (requirement 28).
func TestPruneRequestsForgetsOldIDs(t *testing.T) {
	w, _ := requestWriter(t)
	now := time.Unix(1_800_000_000, 0)
	w.Now = func() time.Time { return now }
	if err := w.FinishRequest(context.Background(), "old", "Thing", nil); err != nil {
		t.Fatal(err)
	}
	now = now.Add(RequestKeep + time.Minute)
	if err := w.FinishRequest(context.Background(), "new", "Thing", nil); err != nil {
		t.Fatal(err)
	}
	if n, err := w.PruneRequests(context.Background(), now.Add(-RequestKeep)); err != nil || n != 1 {
		t.Fatalf("pruned %d, %v; want 1", n, err)
	}
	for id, want := range map[string]bool{"old": false, "new": true} {
		if _, done, err := w.RequestDone(context.Background(), id); err != nil || done != want {
			t.Fatalf("%s done = %v, %v; want %v", id, done, err, want)
		}
	}
}

// The mutation and its opaque response survive together, even when the caller
// never reaches the later FinishRequest step. A replay applies and publishes
// nothing, and can return the exact response after closing and reopening the
// database, before any later FinishRequest call.
func TestFinishRequestInCommitsResultWithMutation(t *testing.T) {
	w, published := requestWriter(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	call := ForRequestCall(ctx, "claim-1", "RunnerClaimInput", []byte("claim-1"))
	want := []byte(" \n{\"batch\":[\"input-1\"],\"cursor\":7}\t")
	ev := hub.Event{Topic: "things"}
	if err := w.Write(call, ev, func(conn *sql.Conn) error {
		if err := insertThing(conn); err != nil {
			return err
		}
		return w.FinishRequestIn(call, conn, "RunnerClaimInput", want)
	}); err != nil {
		t.Fatal(err)
	}
	var file string
	if err := w.db.QueryRowContext(ctx, "SELECT file FROM pragma_database_list WHERE name = 'main'").Scan(&file); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Dir(file)
	if err := database.Close(dataDir); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if db == w.db {
		t.Fatal("reopen reused the closed database handle")
	}
	recovered := newWriter(db)
	recovered.SetPublish(func(ev hub.Event) { *published = append(*published, ev) })
	if got, done, err := recovered.RequestDone(ctx, "claim-1"); err != nil || !done || !bytes.Equal(got, want) {
		t.Fatalf("committed response = %q, %v, %v; want %q", got, done, err, want)
	}
	var method string
	if err := recovered.db.QueryRowContext(ctx, "SELECT method FROM request_ids WHERE id = ?", "claim-1").Scan(&method); err != nil || method != "RunnerClaimInput" {
		t.Fatalf("committed method = %q, %v", method, err)
	}
	called := false
	if err := recovered.Write(ForRequestCall(ctx, "claim-1", "RunnerClaimInput", []byte("claim-1")), ev, func(conn *sql.Conn) error {
		called = true
		return insertThing(conn)
	}); !errors.Is(err, ErrRepeated) || called {
		t.Fatalf("replay = %v, callback ran = %v; want ErrRepeated without a callback", err, called)
	}
	if n := countThings(t, recovered); n != 1 || len(*published) != 1 {
		t.Fatalf("%d mutations and %d events; want one of each, with none on replay", n, len(*published))
	}
	if got, done, err := recovered.RequestDone(ctx, "claim-1"); err != nil || !done || !bytes.Equal(got, want) {
		t.Fatalf("replay response = %q, %v, %v; want %q", got, done, err, want)
	}
}

func TestFinishRequestInRollsBackWithMutation(t *testing.T) {
	w, published := requestWriter(t)
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	call := ForRequestCall(ctx, "claim-rollback", "RunnerClaimInput", []byte("claim-rollback"))
	want := []byte(`{"batch":["input-2"],"cursor":8}`)
	write := func(conn *sql.Conn) error {
		if err := insertThing(conn); err != nil {
			return err
		}
		return w.FinishRequestIn(call, conn, "RunnerClaimInput", want)
	}
	fail := errors.New("abort after saving response")
	ev := hub.Event{Topic: "things"}
	if err := w.Write(call, ev, func(conn *sql.Conn) error {
		if err := write(conn); err != nil {
			return err
		}
		return fail
	}); !errors.Is(err, fail) {
		t.Fatalf("failed transaction = %v; want %v", err, fail)
	}
	if got, done, err := w.RequestDone(ctx, "claim-rollback"); err != nil || done || got != nil {
		t.Fatalf("rolled back response = %q, %v, %v; want no request row", got, done, err)
	}
	if n := countThings(t, w); n != 0 || len(*published) != 0 {
		t.Fatalf("rollback left %d mutations and %d events; want none", n, len(*published))
	}
	// The same call context can retry after its transaction rolled back.
	if err := w.Write(call, ev, write); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
	if got, done, err := w.RequestDone(ctx, "claim-rollback"); err != nil || !done || !bytes.Equal(got, want) {
		t.Fatalf("retry response = %q, %v, %v; want %q", got, done, err, want)
	}
	if n := countThings(t, w); n != 1 || len(*published) != 1 {
		t.Fatalf("retry left %d mutations and %d events; want one of each", n, len(*published))
	}
}

func TestFinishRequestInWithoutRequestIsNoOp(t *testing.T) {
	for _, name := range []string{"direct", "cleared"} {
		t.Run(name, func(t *testing.T) {
			// Direct calls need no request_ids table.
			w, published := testWriter(t)
			ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
			defer cancel()
			if name == "cleared" {
				ctx = ForRequest(ForRequest(ctx, "outer"), "")
			}
			if err := w.Write(ctx, hub.Event{Topic: "things"}, func(conn *sql.Conn) error {
				if err := insertThing(conn); err != nil {
					return err
				}
				return w.FinishRequestIn(ctx, conn, "RunnerClaimInput", []byte(`{"cursor":9}`))
			}); err != nil {
				t.Fatal(err)
			}
			if n := countThings(t, w); n != 1 || len(*published) != 1 {
				t.Fatalf("direct write left %d mutations and %d events; want one of each", n, len(*published))
			}
		})
	}
}
