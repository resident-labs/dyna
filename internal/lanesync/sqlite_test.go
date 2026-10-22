package lanesync

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

func TestUpdateUsesStateWriter(t *testing.T) {
	s := Service{Dir: t.TempDir()}
	db, err := database.Open(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(s.Dir) })
	var events []hub.Event
	state.For(db).SetPublish(func(event hub.Event) { events = append(events, event) })
	if err := s.Update(func(st *Snapshot) error {
		st.Operations["repo"] = Operation{Progress: Progress{State: "building"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("published %v; room watch owns sync events", events)
	}
	// The update, then the rollback copy's pending and final stamps.
	if got := state.For(db).Health().Writes; got != 3 {
		t.Fatalf("state writer saw %d writes, want 3: the update bypassed it", got)
	}
	failed := errors.New("stop")
	if err := s.Update(func(st *Snapshot) error { delete(st.Operations, "repo"); return failed }); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	st, err := s.Read()
	if err != nil || st.Operations["repo"].State != "building" || len(events) != 0 {
		t.Fatalf("failed update changed state or published: %+v, %v, %v", st, err, events)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM lane_sync").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("stored %d rows; want the single current snapshot", count)
	}
}

func TestMigrationAndRollbackCopy(t *testing.T) {
	s := Service{Dir: t.TempDir()}
	t.Cleanup(func() { database.Close(s.Dir) })
	old := Snapshot{Operations: map[string]Operation{"repo": {Progress: Progress{State: "testing"}}}, Exports: map[string]Export{"repo": {Commit: "export"}}}
	if err := store.WriteJSON(s.path(), old, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(s.Dir); err != nil {
		t.Fatal(err)
	}
	db, err := s.open()
	if err != nil {
		t.Fatal(err)
	}
	revision := func() int64 {
		t.Helper()
		var n int64
		if err := db.QueryRow("SELECT revision FROM lane_sync WHERE id=1").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	first := revision()
	if err := Migrate(s.Dir); err != nil {
		t.Fatal(err)
	}
	if revision() != first {
		t.Fatal("unchanged legacy file was imported twice")
	}
	if err := s.Update(func(st *Snapshot) error {
		st.Operations["repo"] = Operation{Progress: Progress{State: "done"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var copy legacySnapshot
	if err := store.ReadJSON(s.path(), &copy); err != nil {
		t.Fatal(err)
	}
	if copy.Revision == 0 || copy.Operations["repo"].State != "done" || copy.Exports["repo"].Commit != "export" {
		t.Fatalf("rollback copy: %+v", copy)
	}
	written := revision()
	if err := Migrate(s.Dir); err != nil {
		t.Fatal(err)
	}
	if revision() != written {
		t.Fatal("own rollback copy was imported")
	}
	// The previous release rewrites its typed Snapshot and drops the stamp.
	old.Operations["repo"] = Operation{Progress: Progress{State: "failed"}}
	if err := store.WriteJSON(s.path(), old, 0600); err != nil {
		t.Fatal(err)
	}
	st, err := s.Read()
	if err != nil || st.Operations["repo"].State != "done" {
		t.Fatalf("read used the file: %+v, %v", st, err)
	}
	if err := Migrate(s.Dir); err != nil {
		t.Fatal(err)
	}
	st, err = s.Read()
	if err != nil || st.Operations["repo"].State != "failed" || revision() <= written {
		t.Fatalf("old binary's write was not imported: %+v, %v", st, err)
	}
	if err := os.Remove(s.path()); err != nil {
		t.Fatal(err)
	}
	st, err = s.Read()
	if err != nil || st.Exports["repo"].Commit != "export" {
		t.Fatalf("missing copy changed SQLite read: %+v, %v", st, err)
	}
}

func TestCrashBeforeRollbackCopyKeepsCommittedState(t *testing.T) {
	s := Service{Dir: t.TempDir()}
	t.Cleanup(func() { database.Close(s.Dir) })
	if err := store.WriteJSON(s.path(), Snapshot{Events: map[string]string{"old": "file"}}, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(s.Dir); err != nil {
		t.Fatal(err)
	}
	db, err := s.open()
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a committed write followed by a crash before its copy.
	if err := state.For(db).Write(t.Context(), hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(t.Context(), "UPDATE lane_sync SET snapshot=?,revision=revision+1 WHERE id=1", `{"events":{"new":"row"}}`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(s.Dir); err != nil {
		t.Fatal(err)
	}
	st, err := s.Read()
	if err != nil || st.Events["new"] != "row" {
		t.Fatalf("stale imported file replaced committed state: %+v, %v", st, err)
	}
	// A copy written before the stamps moved to state_mirrors carries
	// dynaRevision, and also stays ignored after a crash.
	if err := store.WriteJSON(s.path(), legacySnapshot{Snapshot: Snapshot{Events: map[string]string{"stale": "copy"}}, Revision: 1}, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(s.Dir); err != nil {
		t.Fatal(err)
	}
	st, err = s.Read()
	if err != nil || st.Events["new"] != "row" {
		t.Fatalf("stamped copy replaced committed state: %+v, %v", st, err)
	}
}

func TestCancelledUpdateDoesNotChangeStateOrCopy(t *testing.T) {
	s := Service{Dir: t.TempDir()}
	t.Cleanup(func() { database.Close(s.Dir) })
	if err := s.Update(func(st *Snapshot) error { st.Events["old"] = "value"; return nil }); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.path())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	err = s.update(ctx, func(st *Snapshot) error { called = true; st.Events["new"] = "value"; return nil })
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("cancelled update: %v, called=%v", err, called)
	}
	after, err := os.ReadFile(s.path())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("cancelled update changed copy: %v", err)
	}
}

func TestReadAtOldSchemaDoesNotMigrate(t *testing.T) {
	s := Service{Dir: t.TempDir()}
	db, err := database.Open(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(s.Dir) })
	database.DisownSchema()
	t.Cleanup(database.OwnSchema)
	if _, err := s.Read(); err == nil || !strings.Contains(err.Error(), "only the engine") {
		t.Fatalf("old schema read: %v", err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='lane_sync'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("read migrated schema: %d, %v", count, err)
	}
}

func TestRollbackCopyFailureReportsCommittedState(t *testing.T) {
	s := Service{Dir: t.TempDir()}
	t.Cleanup(func() { database.Close(s.Dir) })
	if err := s.Update(func(st *Snapshot) error { st.Events["before"] = "old"; return nil }); err != nil {
		t.Fatal(err)
	}
	err := s.Update(func(st *Snapshot) error {
		st.Events["after"] = "committed"
		// Make the post-commit copy fail, after the legacy import has finished.
		if err := os.Remove(s.path()); err != nil {
			return err
		}
		return os.Mkdir(s.path(), 0700)
	})
	if err == nil || !strings.Contains(err.Error(), "state committed, but rollback copy failed") {
		t.Fatalf("copy failure: %v", err)
	}
	st, err := s.Read()
	if err != nil || st.Events["after"] != "committed" {
		t.Fatalf("copy failure lost committed state: %+v, %v", st, err)
	}
}

// A damaged sync.json is skipped once (state.ErrSkipForeign): the engine
// starts, the state stays, and a later update goes on.
func TestMigrationSkipsADamagedLegacyFile(t *testing.T) {
	s := Service{Dir: t.TempDir()}
	t.Cleanup(func() { database.Close(s.Dir) })
	if err := os.WriteFile(s.path(), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(s.Dir); err != nil {
		t.Fatalf("a damaged legacy file stopped the start: %v", err)
	}
	st, err := s.Read()
	if err != nil || len(st.Events) != 0 {
		t.Fatalf("the skipped file changed SQLite: %+v, %v", st, err)
	}
	if err := s.Update(func(st *Snapshot) error { st.Events["after"] = "update"; return nil }); err != nil {
		t.Fatalf("a damaged legacy file blocked a later update: %v", err)
	}
}
