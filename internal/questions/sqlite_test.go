package questions

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

func questionRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "questions")
	t.Cleanup(func() {
		if err := database.Close(filepath.Dir(root)); err != nil {
			t.Error(err)
		}
	})
	return root
}

func legacyQuestions(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	q := sample("owner")
	q.ID, q.State, q.GroupKey = "q7", "open", GroupKey(q.Text, q.Options)
	st := State{Version: 1, Next: map[string]int{"room": 7}, Questions: map[string]Question{"room/q7": q}}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "state.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func migratedQuestions(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(filepath.Dir(root), "dyna.db")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("SQLite database missing: %v", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value int
	if err := db.QueryRow("SELECT value FROM meta WHERE key = 'questions.schema'").Scan(&value); err != nil || value != 2 {
		t.Fatalf("schema version: %d, %v", value, err)
	}
}

func TestQuestionsImportLegacyOnce(t *testing.T) {
	root := questionRoot(t)
	legacyQuestions(t, root)
	s := &Store{Root: root}
	st, err := s.Read()
	if err != nil || st.Next["room"] != 7 || st.Questions["room/q7"].Text != sample("owner").Text {
		t.Fatalf("import: %+v, %v", st, err)
	}
	migratedQuestions(t, root)
	if err := os.WriteFile(filepath.Join(root, "state.json"), []byte(`{"version":1,"next":{"room":999}}`), 0600); err != nil {
		t.Fatal(err)
	}
	st, err = (&Store{Root: root}).Read()
	if err != nil || st.Next["room"] != 7 || len(st.Questions) != 1 {
		t.Fatalf("legacy state imported twice: %+v, %v", st, err)
	}
	migratedQuestions(t, root)
}

// This is the version check from the old JSON reader. Keep it independent of
// the new Store.Read path, which must accept a migrated store.
func oldQuestionReader(root string) error {
	data, err := os.ReadFile(filepath.Join(root, "state.json"))
	if err != nil {
		return err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return err
	}
	if st.Version != 1 {
		return fmt.Errorf("unsupported question state version %d", st.Version)
	}
	return nil
}

func TestQuestionImportLeavesSourceReadable(t *testing.T) {
	root := questionRoot(t)
	legacyQuestions(t, root)
	if _, err := (&Store{Root: root}).Read(); err != nil {
		t.Fatal(err)
	}
	if err := oldQuestionReader(root); err != nil {
		t.Fatalf("retained source: %v", err)
	}
}

func TestQuestionSQLiteProcess(t *testing.T) {
	root := os.Getenv("QUESTION_SQLITE_HELPER")
	if root == "" {
		t.Skip("subprocess helper")
	}
	fmt.Println("READY")
	if _, err := bufio.NewReader(os.Stdin).ReadByte(); err != nil {
		t.Fatal(err)
	}
	q, err := (&Store{Root: root}).Ask(sample(os.Getenv("QUESTION_SQLITE_ASKER")), nil)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(q.ID)
}

func TestQuestionSnapshotsReadDuringWriter(t *testing.T) {
	s := &Store{Root: questionRoot(t)}
	if _, err := s.Ask(sample("first"), nil); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unlock := func() { once.Do(func() { close(release) }) }
	defer unlock()
	written := make(chan error, 1)
	go func() { written <- holdWriter(s, sample("second"), entered, release) }()
	select {
	case <-entered:
	case err := <-written:
		t.Fatalf("writer stopped: %v", err)
	case <-time.After(6 * time.Second):
		t.Fatal("writer did not enter")
	}
	read := make(chan error, 1)
	go func() {
		st, err := s.Read()
		if err == nil && len(st.Questions) != 1 {
			err = fmt.Errorf("snapshot contains %d questions; want 1 committed question", len(st.Questions))
		}
		read <- err
	}()
	select {
	case err := <-read:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("ordinary reader blocked behind writer")
	}
	unlock()
	if err := <-written; err != nil {
		t.Fatal(err)
	}
}

func TestQuestionRollbackAndNoopKeepRevision(t *testing.T) {
	s := &Store{Root: questionRoot(t)}
	q, err := s.Ask(sample("owner"), nil)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := s.Revision()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.update(func(st *State) error {
		old := st.Questions[key(q.Room, q.ID)]
		old.Text = "Must roll back"
		st.Questions[key(q.Room, q.ID)] = old
		st.Next[q.Room] = 999
		return fmt.Errorf("refused update")
	}); err == nil {
		t.Fatal("accepted refused update")
	}
	if err := s.update(func(*State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	after, err := s.Revision()
	if err != nil || after != revision {
		t.Fatalf("revision changed on rollback/noop: %s -> %s, %v", revision, after, err)
	}
	st, err := s.Read()
	if err != nil || st.Next[q.Room] != 1 || st.Questions[key(q.Room, q.ID)].Text != q.Text {
		t.Fatalf("rollback state: %+v, %v", st, err)
	}
}

func TestQuestionImportFailurePreservesSource(t *testing.T) {
	root := questionRoot(t)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "state.json")
	if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &Store{Root: root}
	if _, err := s.Read(); err == nil {
		t.Fatal("accepted invalid legacy state")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "{broken" {
		t.Fatalf("failed import replaced source: %q, %v", data, err)
	}
	legacyQuestions(t, root)
	if _, err := s.Read(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	st, err := (&Store{Root: root}).Read()
	if err != nil || st.Next["room"] != 7 || len(st.Questions) != 1 {
		t.Fatalf("missing source changed imported rows: %+v, %v", st, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("missing source was recreated: %v", err)
	}
}

func TestQuestionDatabaseSettingsAndJSONBodies(t *testing.T) {
	s := &Store{Root: questionRoot(t)}
	q, err := s.Ask(sample("owner"), nil)
	if err != nil {
		t.Fatal(err)
	}
	db, err := s.open()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(filepath.Dir(s.Root))
	var mode string
	var timeout int
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal mode: %s, %v", mode, err)
	}
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil || timeout != 5000 {
		t.Fatalf("busy timeout: %d, %v", timeout, err)
	}
	var text string
	if err := db.QueryRow("SELECT json_extract(body,'$.text') FROM questions WHERE room=? AND state='open' AND asker=?", q.Room, q.Asker).Scan(&text); err != nil || text != q.Text {
		t.Fatalf("stored JSON: %q, %v", text, err)
	}
}

func TestInterruptedQuestionImportRetriesUnchangedSource(t *testing.T) {
	root := questionRoot(t)
	legacyQuestions(t, root)
	s := &Store{Root: root}
	db, err := database.Open(filepath.Dir(root))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(filepath.Dir(root))
	if _, err := db.Exec(`CREATE TRIGGER interrupt_import BEFORE INSERT ON meta
WHEN NEW.key='questions.schema' AND NEW.value='2' BEGIN SELECT RAISE(ABORT,'interrupted import'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(); err == nil || !strings.Contains(err.Error(), "interrupted import") {
		t.Fatalf("import interruption: %v", err)
	}
	if err := oldQuestionReader(root); err != nil {
		t.Fatalf("interrupted import changed retained source: %v", err)
	}
	if _, err := db.Exec("DROP TRIGGER interrupt_import"); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.Read()
	if err != nil || recovered.Next["room"] != 7 || len(recovered.Questions) != 1 || recovered.Questions["room/q7"].Text != sample("owner").Text {
		t.Fatalf("source recovery: %+v, %v", recovered, err)
	}
	migratedQuestions(t, root)
}

func TestRepeatedRevisionDoesNotReopenOrReimport(t *testing.T) {
	root := questionRoot(t)
	s := &Store{Root: root}
	if _, err := s.Revision(); err != nil {
		t.Fatal(err)
	}
	first, err := s.open()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(filepath.Dir(root))
	for range 3 {
		if _, err := s.Revision(); err != nil {
			t.Fatal(err)
		}
	}
	second, err := s.open()
	if err != nil {
		t.Fatal(err)
	}

	if first != second {
		t.Fatal("Revision reopened the database")
	}
	if _, err := os.Stat(filepath.Join(root, "state.json")); !os.IsNotExist(err) {
		t.Fatalf("Revision reran the import check: %v", err)
	}
}

// A process that dies after posting a question and before recording the
// post leaves an ordinary open question with no post ID: the next process
// lists it, can answer it, and gives the next question a new ID.
func TestAskCrashBetweenPostAndRecord(t *testing.T) {
	if root := os.Getenv("QUESTION_CRASH_ROOT"); root != "" {
		(&Store{Root: root}).Ask(sample("crasher"), func(Question) (string, error) { os.Exit(3); return "", nil })
		return
	}
	root := questionRoot(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestAskCrashBetweenPostAndRecord$")
	cmd.Env = append(os.Environ(), "QUESTION_CRASH_ROOT="+root)
	var exit *exec.ExitError
	if err := cmd.Run(); !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("helper did not stop inside the post: %v", err)
	}
	s := &Store{Root: root}
	open, err := s.OpenAskedBy("room", "crasher")
	if err != nil || len(open) != 1 || open[0].ID != "q1" || open[0].Post != "" {
		t.Fatalf("open questions after the crash = %+v, %v; want q1 with no post ID", open, err)
	}
	if _, err := s.Answer("room", "q1", Answer{By: "owner", Choice: "yes"}); err != nil {
		t.Fatalf("answer the question left by the crash: %v", err)
	}
	if q, err := s.Ask(sample("crasher"), nil); err != nil || q.ID != "q2" {
		t.Fatalf("next question = %+v, %v; want q2", q, err)
	}
}

func TestQuestionImportPreservesRawSourceAndCanonicalState(t *testing.T) {
	const source = `{"version":1,"next":{"room":7},"questions":{"room/q7":{"id":"q7","room":"room","state":"open","asker":"owner","text":"Keep this","future":{"answer":42}}}}`
	for _, tc := range []struct {
		name, source, backup, canonical string
		wantError                       bool
	}{
		{name: "source", source: source},
		{name: "missing-source", backup: source},
		{name: "guard", source: `{"version":2}`, backup: source},
		{name: "empty"},
		{name: "marker", source: "{stale", canonical: `INSERT INTO meta VALUES('questions.imported','1')`},
		{name: "rows", source: "{stale", canonical: `INSERT INTO questions VALUES('saved','room','open','owner','{"id":"saved","room":"room"}')`},
		{name: "counters", source: "{stale", canonical: `INSERT INTO question_next VALUES('saved',17)`},
		{name: "revision", source: "{stale", canonical: `INSERT INTO meta VALUES('questions.revision','17')`},
		{name: "guard-without-backup", source: `{"version":2}`, wantError: true},
		{name: "invalid-backup", backup: "{broken", wantError: true},
		{name: "invalid-row", source: `{"version":1,"next":{"room":7},"questions":{"bad":{"room":17}}}`, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := questionRoot(t)
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			files := map[string]string{"state.json": tc.source, "state.v1.backup.json": tc.backup}
			for name, body := range files {
				if body != "" {
					if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			db, err := database.Open(filepath.Dir(root))
			if err != nil {
				t.Fatal(err)
			}
			err = state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
				if _, err := conn.ExecContext(context.Background(), questionsSchema); err != nil {
					return err
				}
				if tc.canonical != "" {
					if _, err := conn.ExecContext(context.Background(), `INSERT INTO meta VALUES('questions.schema','1')`); err != nil {
						return err
					}
					if _, err := conn.ExecContext(context.Background(), tc.canonical); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			s := &Store{Root: root}
			st, err := s.Read()
			if (err != nil) != tc.wantError {
				t.Fatalf("import error: %v", err)
			}
			var version, rows, counters, markers int
			if err := db.QueryRow(`SELECT COALESCE((SELECT value FROM meta WHERE key='questions.schema'),0), (SELECT count(*) FROM questions), (SELECT count(*) FROM question_next), (SELECT count(*) FROM meta WHERE key='questions.imported')`).Scan(&version, &rows, &counters, &markers); err != nil {
				t.Fatal(err)
			}
			if tc.wantError {
				if version == 2 || rows != 0 || counters != 0 {
					t.Fatalf("failed import committed: version=%d rows=%d counters=%d", version, rows, counters)
				}
			} else {
				if version != 2 || markers != 0 {
					t.Fatalf("migration: version=%d markers=%d", version, markers)
				}
				if tc.canonical == "" && tc.name != "empty" {
					if st.Next["room"] != 7 || st.Questions["room/q7"].Text != "Keep this" {
						t.Fatalf("import: %+v", st)
					}
					var future int
					if err := db.QueryRow(`SELECT json_extract(body,'$.future.answer') FROM questions WHERE key='room/q7'`).Scan(&future); err != nil || future != 42 {
						t.Fatalf("unknown field: %d, %v", future, err)
					}
				} else if tc.canonical != "" {
					wantRows, wantCounters := 0, 0
					if tc.name == "rows" {
						wantRows = 1
					}
					if tc.name == "counters" {
						wantCounters = 1
					}
					if rows != wantRows || counters != wantCounters || (tc.name == "rows" && st.Questions["saved"].ID != "saved") || (tc.name == "counters" && st.Next["saved"] != 17) {
						t.Fatalf("canonical state replaced: %+v", st)
					}
					revision, err := s.Revision()
					want := "0"
					if tc.name == "revision" {
						want = "17"
					}
					if err != nil || revision != want {
						t.Fatalf("revision %s, %v; want %s", revision, err, want)
					}
				}
				if err := s.update(func(st *State) error { st.Next = map[string]int{}; st.Questions = map[string]Question{}; return nil }); err != nil {
					t.Fatal(err)
				}
				if err := database.Close(filepath.Dir(root)); err != nil {
					t.Fatal(err)
				}
				st, err = (&Store{Root: root}).Read()
				if err != nil || len(st.Questions) != 0 || len(st.Next) != 0 {
					t.Fatalf("retained input restored deleted state: %+v, %v", st, err)
				}
			}
			for name, body := range files {
				got, err := os.ReadFile(filepath.Join(root, name))
				if body == "" {
					if !os.IsNotExist(err) {
						t.Errorf("created %s: %v", name, err)
					}
				} else if err != nil || !bytes.Equal(got, []byte(body)) {
					t.Errorf("changed %s: %q, %v", name, got, err)
				}
			}
		})
	}
}
