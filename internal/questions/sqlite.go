package questions

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"dyna-agent/internal/database"
	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

const questionsSchema = `CREATE TABLE IF NOT EXISTS questions (
 key TEXT PRIMARY KEY, room TEXT NOT NULL, state TEXT NOT NULL,
 asker TEXT NOT NULL, body TEXT NOT NULL CHECK(json_valid(body))
);
CREATE INDEX IF NOT EXISTS questions_room ON questions(room);
CREATE INDEX IF NOT EXISTS questions_open_asker ON questions(state,room,asker);
CREATE TABLE IF NOT EXISTS question_next (room TEXT PRIMARY KEY, next INTEGER NOT NULL);`

// Migrate creates the questions tables and imports the old JSON store at
// engine start. Ordinary clients only check the completed migration.
func Migrate(dataDir string) error {
	_, err := (&Store{Root: filepath.Join(dataDir, "questions")}).open()
	return err
}

func (s *Store) open() (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.db, nil
	}
	db, err := database.Open(filepath.Dir(s.Root))
	if err != nil {
		return nil, err
	}
	if err := database.Migrate(db, "questions.schema", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), questionsSchema)
		return err
	}); err != nil {
		return nil, err
	}
	if database.OwnsSchema() {
		if err := s.importLegacy(db, s.installGuard); err != nil {
			return nil, err
		}
	} else {
		done, err := importDone(db)
		if err != nil {
			return nil, err
		}
		if !done {
			return nil, fmt.Errorf("only the engine imports questions; restart it: systemctl --user restart dyna-engine.service")
		}
	}
	s.db = db
	return db, nil
}

func importDone(db *sql.DB) (bool, error) {
	var value string
	err := db.QueryRow("SELECT value FROM meta WHERE key='questions.imported'").Scan(&value)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func guardInstalled(root string) bool {
	data, err := os.ReadFile(filepath.Join(root, "state.json"))
	if err != nil {
		return false
	}
	var state struct {
		Version int `json:"version"`
	}
	return json.Unmarshal(data, &state) == nil && state.Version == 2
}

// TODO: legacy/shim remove later — imports version-1 questions and excludes old writers until the guard is installed; remove when supported binaries all use dyna.db.
func (s *Store) importLegacy(db *sql.DB, installGuard func() error) error {
	done, err := importDone(db)
	if err != nil {
		return err
	}
	if done && guardInstalled(s.Root) {
		return nil
	}
	if err := os.MkdirAll(s.Root, 0700); err != nil {
		return err
	}
	unlock, err := store.Lock(filepath.Join(s.Root, "state.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	if err := state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		ctx := context.Background()
		var marker string
		err := conn.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='questions.imported'").Scan(&marker)
		if err == nil {
			return nil
		}
		if err != sql.ErrNoRows {
			return err
		}
		st, err := s.prepareLegacyImport(installGuard)
		if err != nil {
			return err
		}
		if err := importQuestionRows(conn, st); err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES ('questions.imported','1'),('questions.revision','0')")
		return err
	}); err != nil {
		return err
	}
	if !guardInstalled(s.Root) {
		return installGuard()
	}
	return nil
}

// TODO: legacy/shim remove later — preserves old questions across interrupted imports; remove after retained stores are migrated.
func (s *Store) prepareLegacyImport(installGuard func() error) (legacyQuestionState, error) {
	source := filepath.Join(s.Root, "state.json")
	backup := filepath.Join(s.Root, "state.v1.backup.json")
	// Version 1 remains authoritative until the guard is installed. Recapture
	// it after a failed guard write, since an old writer may have accepted work.
	if !guardInstalled(s.Root) {
		if _, err := os.Stat(source); os.IsNotExist(err) {
			if _, err := os.Stat(backup); err == nil {
				source = backup
			}
		}
		st, err := readLegacy(source)
		if err != nil {
			return legacyQuestionState{}, err
		}
		data, err := json.Marshal(st)
		if err != nil {
			return legacyQuestionState{}, err
		}
		if err := s.writeMigrationFile("state.v1.backup.json", append(data, '\n')); err != nil {
			return legacyQuestionState{}, err
		}
	}
	st, err := readLegacy(backup)
	if err != nil {
		return legacyQuestionState{}, err
	}
	if _, err := os.Stat(backup); err != nil {
		return legacyQuestionState{}, fmt.Errorf("question import backup: %w", err)
	}
	// The backup is durable before an old reader loses access to version 1.
	// The guard is durable before the database can commit its import marker.
	if err := installGuard(); err != nil {
		return legacyQuestionState{}, err
	}
	return st, nil
}

func (s *Store) installGuard() error {
	// TODO: legacy/shim remove later — makes pre-SQLite binaries reject migrated questions; remove when no supported binary reads state.json.
	return s.writeMigrationFile("state.json", []byte("{\"version\":2}\n"))
}

// TODO: legacy/shim remove later — preserves the one-time import backup and old-binary guard; remove with the questions JSON reader when requirement 31 ends.
func (s *Store) writeMigrationFile(name string, data []byte) error {
	if err := fsutil.WriteFile(filepath.Join(s.Root, name), data, 0600, true); err != nil {
		return err
	}
	directory, err := os.Open(s.Root)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// legacyQuestionState retains each complete question document during import.
type legacyQuestionState struct {
	Version   int                        `json:"version"`
	Next      map[string]int             `json:"next"`
	Questions map[string]json.RawMessage `json:"questions"`
}

func importQuestionRows(conn *sql.Conn, st legacyQuestionState) error {
	ctx := context.Background()
	for key, body := range st.Questions {
		var routing struct {
			Room  string `json:"room"`
			State string `json:"state"`
			Asker string `json:"asker"`
		}
		if err := json.Unmarshal(body, &routing); err != nil {
			return fmt.Errorf("invalid question %s: %w", key, err)
		}
		if _, err := conn.ExecContext(ctx, "INSERT INTO questions(key,room,state,asker,body) VALUES(?,?,?,?,?)", key, routing.Room, routing.State, routing.Asker, string(body)); err != nil {
			return err
		}
	}
	for room, next := range st.Next {
		if _, err := conn.ExecContext(ctx, "INSERT INTO question_next(room,next) VALUES(?,?)", room, next); err != nil {
			return err
		}
	}
	return nil
}

// TODO: legacy/shim remove later — reads the old question JSON only for its one-time import; remove after all retained stores are migrated.
func readLegacy(path string) (legacyQuestionState, error) {
	st := legacyQuestionState{Version: 1, Next: map[string]int{}, Questions: map[string]json.RawMessage{}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("invalid question state: %w", err)
	}
	if st.Version != 1 {
		return st, fmt.Errorf("unsupported question state version %d", st.Version)
	}
	if st.Next == nil {
		st.Next = map[string]int{}
	}
	if st.Questions == nil {
		st.Questions = map[string]json.RawMessage{}
	}
	return st, nil
}

func readState(query state.Reader) (State, error) {
	st := State{Version: 1, Next: map[string]int{}, Questions: map[string]Question{}}
	rows, err := query.QueryContext(context.Background(), "SELECT room,next FROM question_next")
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var room string
		var next int
		if err := rows.Scan(&room, &next); err != nil {
			rows.Close()
			return st, err
		}
		st.Next[room] = next
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return st, err
	}
	rows, err = query.QueryContext(context.Background(), "SELECT key,body FROM questions")
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var body []byte
		var q Question
		if err := rows.Scan(&key, &body); err != nil {
			return st, err
		}
		if err := json.Unmarshal(body, &q); err != nil {
			return st, err
		}
		st.Questions[key] = q
	}
	return st, rows.Err()
}

// saveState writes changed rows only. Callers hold BEGIN IMMEDIATE.
func saveState(conn *sql.Conn, before, after State) (bool, error) {
	changed := false
	ctx := context.Background()
	for key := range before.Questions {
		if _, ok := after.Questions[key]; !ok {
			if _, err := conn.ExecContext(ctx, "DELETE FROM questions WHERE key=?", key); err != nil {
				return false, err
			}
			changed = true
		}
	}
	for key, q := range after.Questions {
		body, err := json.Marshal(q)
		if err != nil {
			return false, err
		}
		if old, ok := before.Questions[key]; ok {
			previous, err := json.Marshal(old)
			if err != nil {
				return false, err
			}
			if bytes.Equal(previous, body) {
				continue
			}
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO questions(key,room,state,asker,body) VALUES (?,?,?,?,?)
ON CONFLICT(key) DO UPDATE SET room=excluded.room,state=excluded.state,asker=excluded.asker,body=excluded.body`, key, q.Room, q.State, q.Asker, string(body)); err != nil {
			return false, err
		}
		changed = true
	}
	for room := range before.Next {
		if _, ok := after.Next[room]; !ok {
			if _, err := conn.ExecContext(ctx, "DELETE FROM question_next WHERE room=?", room); err != nil {
				return false, err
			}
			changed = true
		}
	}
	for room, next := range after.Next {
		if old, ok := before.Next[room]; ok && old == next {
			continue
		}
		if _, err := conn.ExecContext(ctx, "INSERT INTO question_next(room,next) VALUES (?,?) ON CONFLICT(room) DO UPDATE SET next=excluded.next", room, next); err != nil {
			return false, err
		}
		changed = true
	}
	return changed, nil
}

func (s *Store) Read() (State, error) {
	db, err := s.open()
	if err != nil {
		return State{}, err
	}
	var st State
	err = state.For(db).Read(context.Background(), func(query state.Reader) error {
		var err error
		st, err = readState(query)
		return err
	})
	return st, err
}

// ReadLocked waits for a writer before reading. Lifecycle checks need this
// barrier; ordinary Read uses a nonblocking WAL snapshot instead.
func (s *Store) ReadLocked() (State, error) {
	db, err := s.open()
	if err != nil {
		return State{}, err
	}
	var st State
	err = state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error { var err error; st, err = readState(conn); return err })
	return st, err
}

func (s *Store) update(fn func(*State) error) error {
	return s.updateTx(func(_ *sql.Conn, st *State) error { return fn(st) })
}

// updateTx is update for changes that also write other dyna.db tables on
// the same transaction.
func (s *Store) updateTx(fn func(*sql.Conn, *State) error) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	// TODO: legacy/shim remove later — the room watcher publishes questions; remove when requirement 16 moves this topic to the write.
	return state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		st, err := readState(conn)
		if err != nil {
			return err
		}
		before := State{Next: map[string]int{}, Questions: map[string]Question{}}
		for room, next := range st.Next {
			before.Next[room] = next
		}
		for key, q := range st.Questions {
			old := q
			if q.Answer != nil {
				answer := *q.Answer
				old.Answer = &answer
			}
			if q.Withdrawn != nil {
				withdrawn := *q.Withdrawn
				old.Withdrawn = &withdrawn
			}
			before.Questions[key] = old
		}
		if err := fn(conn, &st); err != nil {
			return err
		}
		changed, err := saveState(conn, before, st)
		if err != nil || !changed {
			return err
		}
		_, err = conn.ExecContext(context.Background(), "UPDATE meta SET value=CAST(value AS INTEGER)+1 WHERE key='questions.revision'")
		return err
	})
}

// Revision changes only when question rows or ID counters change.
func (s *Store) Revision() (string, error) {
	db, err := s.open()
	if err != nil {
		return "", err
	}
	var version string
	err = db.QueryRow("SELECT value FROM meta WHERE key='questions.revision'").Scan(&version)
	return version, err
}

func (s *Store) listQuestions(where string, locked bool, args ...any) ([]Question, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	out := []Question{}
	read := func(query state.Reader) error {
		rows, err := query.QueryContext(context.Background(), "SELECT body FROM questions WHERE "+where, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var body []byte
			if err := rows.Scan(&body); err != nil {
				return err
			}
			var question Question
			if err := json.Unmarshal(body, &question); err != nil {
				return err
			}
			out = append(out, question)
		}
		return rows.Err()
	}
	if locked {
		err = state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error { return read(conn) })
	} else {
		err = state.For(db).Read(context.Background(), read)
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AskedAt.After(out[j].AskedAt) })
	return out, nil
}
