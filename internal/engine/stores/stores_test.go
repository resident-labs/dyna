package stores

import (
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/account/attempts"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

func TestMirrorStampsMigrateFirst(t *testing.T) {
	if reflect.ValueOf(migrations[0]).Pointer() != reflect.ValueOf(state.MigrateMirrors).Pointer() {
		t.Fatal("state.MigrateMirrors must be the first store migration")
	}
}

func TestAdmissionFactsUseCanonicalParentsAfterStartup(t *testing.T) {
	dir := t.TempDir()
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	writeSQL := func(query string) {
		t.Helper()
		if err := state.For(db).Write(t.Context(), hub.Event{}, func(conn *sql.Conn) error {
			_, err := conn.ExecContext(t.Context(), query)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	// These rows use the actual schemas created by startup, with the
	// composite session-turn key and the run ID key. No fixture DDL runs.
	writeSQL(`INSERT INTO session_turns(session,n,revision,turn) VALUES ('s',1,1,'{}');
INSERT INTO runs(id,session,started,body) VALUES ('r','s',0,'{"status":"running"}');`)
	refs := []callspec.ExecutionRef{
		{Session: "s", Turn: 1, AdmissionID: "s/request/1"},
		{Run: "r", Agent: 1, AdmissionID: "r/request/1"},
	}
	for _, ref := range refs {
		fact := attempts.Fact{ExecutionRef: ref, AccountID: "a", Identity: "owner-a",
			Profile: "p", Harness: "muse", WorkDir: "/work", AdmittedAt: time.Unix(1, 0).UTC()}
		if err := state.For(db).Write(t.Context(), hub.Event{}, func(conn *sql.Conn) error {
			_, err := attempts.SaveTx(t.Context(), conn, fact)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if got, err := attempts.Load(t.Context(), dir, ref); err != nil || got != fact {
			t.Fatalf("startup admission = %+v, %v; want %+v", got, err, fact)
		}
	}
	for _, ref := range []callspec.ExecutionRef{
		{Session: "s", Turn: 2, AdmissionID: "missing-turn"},
		{Run: "missing", Agent: 1, AdmissionID: "missing-run"},
	} {
		err := state.For(db).Write(t.Context(), hub.Event{}, func(conn *sql.Conn) error {
			_, err := attempts.SaveTx(t.Context(), conn, attempts.Fact{ExecutionRef: ref})
			return err
		})
		if err == nil {
			t.Fatalf("startup accepted a nonexistent canonical parent: %+v", ref)
		}
	}
	writeSQL("DELETE FROM session_turns WHERE session='s' AND n=1")
	if _, err := attempts.Load(t.Context(), dir, refs[0]); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("session-turn deletion did not cascade: %v", err)
	}
	if _, err := attempts.Load(t.Context(), dir, refs[1]); err != nil {
		t.Fatalf("session-turn deletion removed the run fact: %v", err)
	}
	writeSQL("DELETE FROM runs WHERE id='r'")
	if _, err := attempts.Load(t.Context(), dir, refs[1]); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("run deletion did not cascade: %v", err)
	}
}

func TestMigrationFailureKeepsTheCause(t *testing.T) {
	saved := migrations
	t.Cleanup(func() { migrations = saved })
	cause := errors.New("broken store")
	secondRan := false
	migrations = []func(string) error{func(string) error { return cause }, func(string) error { secondRan = true; return nil }}
	err := Migrate(t.TempDir())
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "migrate dyna.db") || secondRan {
		t.Fatalf("migration failure = %v, next store ran = %v", err, secondRan)
	}
}
