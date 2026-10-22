package attempts

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

func fixture(t *testing.T) (string, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(dir); err != nil {
			t.Error(err)
		}
	})
	// Minimal fixtures use the canonical keys from subagent/turns.go,
	// runstore/meta_db.go, and account/store/registry.go. Production never
	// creates or migrates these parents through the admission store.
	execSQL(t, db, `CREATE TABLE session_turns (
 session TEXT NOT NULL, n INTEGER NOT NULL, revision INTEGER NOT NULL,
 turn BLOB NOT NULL, PRIMARY KEY(session,n));
CREATE TABLE runs (id TEXT PRIMARY KEY, session TEXT NOT NULL,
 started INTEGER NOT NULL, body TEXT NOT NULL CHECK(json_valid(body)));
CREATE TABLE accounts (id TEXT PRIMARY KEY, ord INTEGER NOT NULL,
 metadata BLOB NOT NULL CHECK(json_valid(metadata)));
INSERT INTO session_turns VALUES ('s',1,1,'{"status":"running"}'), ('s',2,2,'{}');
INSERT INTO runs VALUES ('r','s',0,'{"status":"running"}'), ('other','s',0,'{}');
INSERT INTO accounts VALUES ('a',0,'{"identity":"owner-a"}');`)
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dir); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	return dir, db
}

func execSQL(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if err := state.For(db).Write(t.Context(), hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(t.Context(), query, args...)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func sample(run bool) Fact {
	ref := callspec.ExecutionRef{Session: "s", Turn: 1, AdmissionID: "s/full/request/1"}
	if run {
		ref = callspec.ExecutionRef{Run: "r", Agent: 1, AdmissionID: "r/full/request/1"}
	}
	return Fact{ExecutionRef: ref, AccountID: "a", Identity: "owner-a", Profile: "p",
		Harness: "muse", Repo: "github.com/org/repo", WorkDir: "/work/repo",
		AdmittedAt: time.Date(2026, 10, 21, 4, 0, 0, 123456789, time.UTC)}
}

func save(t *testing.T, db *sql.DB, fact Fact) (Fact, error) {
	t.Helper()
	var got Fact
	err := state.For(db).Write(t.Context(), hub.Event{}, func(conn *sql.Conn) error {
		var err error
		got, err = SaveTx(t.Context(), conn, fact)
		return err
	})
	return got, err
}

func assertFact(t *testing.T, got, want Fact) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fact = %+v; want %+v", got, want)
	}
}

func TestFactReplay(t *testing.T) {
	dir, db := fixture(t)
	for _, run := range []bool{false, true} {
		want := sample(run)
		got, err := save(t, db, want)
		if err != nil {
			t.Fatal(err)
		}
		assertFact(t, got, want)
		replay := want
		replay.AdmittedAt = want.AdmittedAt.Add(time.Hour)
		got, err = save(t, db, replay)
		if err != nil {
			t.Fatal(err)
		}
		assertFact(t, got, want)
		got, err = Load(t.Context(), dir, want.ExecutionRef)
		if err != nil {
			t.Fatal(err)
		}
		assertFact(t, got, want)
		// A new full request ID is a distinct attempt in the same execution.
		next := want
		next.AdmissionID += "/next"
		next.AccountID, next.Identity = "b", "owner-b"
		next.Repo = ""
		if _, err := save(t, db, next); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM account_attempts").Scan(&count); err != nil || count != 4 {
		t.Fatalf("row count = %d, %v; want 4", count, err)
	}
}

func TestFactRejectsChangedClientFacts(t *testing.T) {
	dir, db := fixture(t)
	for _, run := range []bool{false, true} {
		original := sample(run)
		if _, err := save(t, db, original); err != nil {
			t.Fatal(err)
		}
		for _, change := range []struct {
			name string
			edit func(*Fact)
		}{
			{"account", func(f *Fact) { f.AccountID = "b" }},
			{"identity", func(f *Fact) { f.Identity = "owner-b" }},
			{"profile", func(f *Fact) { f.Profile = "other" }},
			{"harness", func(f *Fact) { f.Harness = "codex" }},
			{"repo", func(f *Fact) { f.Repo = "" }},
			{"workdir", func(f *Fact) { f.WorkDir += "/child" }},
			{"execution", func(f *Fact) {
				if run {
					f.Run = "other"
				} else {
					f.Turn = 2
				}
			}},
			{"agent", func(f *Fact) {
				if run {
					f.Agent++
				} else {
					f.Session = "other"
				}
			}},
		} {
			changed := original
			change.edit(&changed)
			if _, err := save(t, db, changed); !errors.Is(err, ErrConflict) {
				t.Fatalf("changed %s for run=%v: %v; want ErrConflict", change.name, run, err)
			}
		}
		got, err := Load(t.Context(), dir, original.ExecutionRef)
		if err != nil {
			t.Fatal(err)
		}
		assertFact(t, got, original)
	}
}

func TestFactRequiresMatchingReference(t *testing.T) {
	dir, db := fixture(t)
	original := sample(false)
	if _, err := save(t, db, original); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []callspec.ExecutionRef{
		{}, {Session: "s", Turn: 1},
		{Session: "s", Turn: 2, AdmissionID: original.AdmissionID},
		{Session: "other", Turn: 1, AdmissionID: original.AdmissionID},
		{Run: "r", Agent: 1, AdmissionID: original.AdmissionID},
		{Session: "s", Turn: 1, Run: "r", Agent: 1, AdmissionID: original.AdmissionID},
	} {
		if _, err := Load(t.Context(), dir, ref); err == nil {
			t.Fatalf("loaded mismatched reference %+v", ref)
		}
	}
	missing := original.ExecutionRef
	missing.AdmissionID = "missing"
	if _, err := Load(t.Context(), dir, missing); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing admission = %v; want sql.ErrNoRows", err)
	}
}

func TestFactRejectsInvalidParents(t *testing.T) {
	_, db := fixture(t)
	for _, ref := range []callspec.ExecutionRef{
		{}, {Session: "s"}, {Session: "s", Turn: -1},
		{Run: "r"}, {Run: "r", Agent: -1},
		{Session: "s", Turn: 1, Run: "r", Agent: 1},
		{Session: "missing", Turn: 1}, {Session: "s", Turn: 3},
		{Run: "missing", Agent: 1},
	} {
		fact := sample(false)
		fact.ExecutionRef = ref
		fact.AdmissionID = "invalid"
		if _, err := save(t, db, fact); err == nil {
			t.Fatalf("saved invalid parent %+v", ref)
		}
	}
	missingID := sample(false)
	missingID.AdmissionID = ""
	if _, err := save(t, db, missingID); err == nil {
		t.Fatal("saved without an admission ID")
	}
	// Bypass Go validation to prove the relation itself enforces parent shape
	// and existence. NULL must fail rather than pass a three-valued CHECK.
	for _, parent := range [][4]any{
		{nil, nil, nil, nil}, {"s", nil, nil, nil}, {"s", 0, nil, nil},
		{nil, nil, "r", nil}, {nil, nil, "r", 0}, {"s", 1, "r", 1},
		{"", 1, nil, nil}, {nil, nil, "", 1}, {"s", 1, nil, 0},
		{nil, 0, "r", 1}, {"s", -1, nil, nil}, {nil, nil, "r", -1},
		{"missing", 1, nil, nil}, {"s", 3, nil, nil}, {nil, nil, "missing", 1},
	} {
		err := state.For(db).Write(t.Context(), hub.Event{}, func(conn *sql.Conn) error {
			_, err := conn.ExecContext(t.Context(), `INSERT INTO account_attempts
(admission_id,session,turn,run,agent,account_id,identity,profile,harness,repo,work_dir,admitted_at)
VALUES ('invalid',?,?,?,?, 'a','owner-a','p','muse','','/work',1)`, parent[:]...)
			return err
		})
		if err == nil {
			t.Fatalf("SQL accepted invalid parent %v", parent)
		}
	}
}

func TestFactParentDeleteCascades(t *testing.T) {
	dir, db := fixture(t)
	for _, run := range []bool{false, true} {
		if _, err := save(t, db, sample(run)); err != nil {
			t.Fatal(err)
		}
	}
	execSQL(t, db, "DELETE FROM session_turns WHERE session='s' AND n=2; DELETE FROM runs WHERE id='other'")
	for _, run := range []bool{false, true} {
		if _, err := Load(t.Context(), dir, sample(run).ExecutionRef); err != nil {
			t.Fatalf("unrelated deletion removed fact: %v", err)
		}
	}
	execSQL(t, db, "DELETE FROM session_turns WHERE session='s' AND n=1")
	if _, err := Load(t.Context(), dir, sample(false).ExecutionRef); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("turn deletion left its fact: %v", err)
	}
	if _, err := Load(t.Context(), dir, sample(true).ExecutionRef); err != nil {
		t.Fatalf("turn deletion removed run fact: %v", err)
	}
	execSQL(t, db, "DELETE FROM runs WHERE id='r'")
	if _, err := Load(t.Context(), dir, sample(true).ExecutionRef); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("run deletion left its fact: %v", err)
	}
}

func TestFactRetainsHistoryAcrossReopenAndArchive(t *testing.T) {
	dir, db := fixture(t)
	for _, run := range []bool{false, true} {
		if _, err := save(t, db, sample(run)); err != nil {
			t.Fatal(err)
		}
	}
	execSQL(t, db, `UPDATE session_turns SET turn='{"status":"ok"}' WHERE session='s';
UPDATE runs SET body='{"status":"ok"}' WHERE id='r';
DELETE FROM accounts WHERE id='a';
INSERT INTO accounts VALUES ('a',0,'{"identity":"different-owner"}');`)
	// ArchiveRun moves the run directory without deleting its canonical row.
	live, archived := filepath.Join(dir, "runs", "r"), filepath.Join(dir, "archive", "runs", "r")
	if err := os.MkdirAll(live, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(archived), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(live, archived); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(dir); err != nil {
		t.Fatal(err)
	}
	for _, run := range []bool{false, true} {
		want := sample(run)
		got, err := Load(t.Context(), dir, want.ExecutionRef)
		if err != nil {
			t.Fatal(err)
		}
		assertFact(t, got, want)
	}
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	replay := sample(true)
	replay.AdmittedAt = replay.AdmittedAt.Add(time.Hour)
	got, err := save(t, db, replay)
	if err != nil {
		t.Fatal(err)
	}
	assertFact(t, got, sample(true))
	// Reads must not create an admission migration in an unmigrated store.
	empty := t.TempDir()
	other, err := database.Open(empty)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(empty) })
	if _, err := Load(context.Background(), empty, sample(false).ExecutionRef); err == nil {
		t.Fatal("loaded from unmigrated store")
	}
	var tables int
	if err := other.QueryRow("SELECT COUNT(*) FROM sqlite_schema WHERE name='account_attempts'").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("read migrated storage: table count %d, error %v", tables, err)
	}
}

func TestFactSharesEngineTransaction(t *testing.T) {
	dir, db := fixture(t)
	abort := errors.New("engine refused admission")
	err := state.For(db).Write(t.Context(), hub.Event{}, func(conn *sql.Conn) error {
		if _, err := SaveTx(t.Context(), conn, sample(false)); err != nil {
			return err
		}
		return abort
	})
	if !errors.Is(err, abort) {
		t.Fatalf("write error = %v; want engine refusal", err)
	}
	if _, err := Load(t.Context(), dir, sample(false).ExecutionRef); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rolled back admission remained: %v", err)
	}
}

func TestFactMigrationRequiresCanonicalParents(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	if err := Migrate(dir); err == nil {
		t.Fatal("migration succeeded without canonical parents")
	}
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema
WHERE name IN ('account_attempts','session_turns','runs')`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("migration created child or replacement parents: %d tables, %v", tables, err)
	}
}
