// Package librarianstate stores repository librarian identity and scheduling state.
package librarianstate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

const schemaVersion = 2

const schema = `CREATE TABLE librarian_repos (
	repo TEXT PRIMARY KEY,
	main TEXT NOT NULL,
	room_id TEXT NOT NULL,
	session TEXT NOT NULL DEFAULT '',
	head TEXT NOT NULL DEFAULT '',
	pending TEXT NOT NULL DEFAULT '',
	pending_turn INTEGER NOT NULL DEFAULT 0,
	last_run TEXT NOT NULL DEFAULT '',
	failed_profiles TEXT NOT NULL DEFAULT '{}',
	smell_day TEXT NOT NULL DEFAULT '',
	smells INTEGER NOT NULL DEFAULT 0,
	smell_session TEXT NOT NULL DEFAULT '',
	smell_head TEXT NOT NULL DEFAULT '',
	smell_pending TEXT NOT NULL DEFAULT '',
	smell_pending_turn INTEGER NOT NULL DEFAULT 0,
	smell_last_run TEXT NOT NULL DEFAULT '',
	smell_failed_profiles TEXT NOT NULL DEFAULT '{}'
);`

const smellPassSchema = `ALTER TABLE librarian_repos ADD COLUMN smell_session TEXT NOT NULL DEFAULT '';
ALTER TABLE librarian_repos ADD COLUMN smell_head TEXT NOT NULL DEFAULT '';
ALTER TABLE librarian_repos ADD COLUMN smell_pending TEXT NOT NULL DEFAULT '';
ALTER TABLE librarian_repos ADD COLUMN smell_pending_turn INTEGER NOT NULL DEFAULT 0;
ALTER TABLE librarian_repos ADD COLUMN smell_last_run TEXT NOT NULL DEFAULT '';
ALTER TABLE librarian_repos ADD COLUMN smell_failed_profiles TEXT NOT NULL DEFAULT '{}';`

// State is one repository's librarian scheduling state.
type State struct {
	Repo    string `json:"repo"`
	Main    string `json:"main"`
	RoomID  string `json:"roomId"`
	Session string `json:"session,omitempty"`
	// Head is the last main commit a librarian turn finished or the gate
	// skipped. Pending is the commit of the assigned turn PendingTurn; it
	// becomes Head only when that turn succeeds.
	Head                string               `json:"head,omitempty"`
	Pending             string               `json:"pending,omitempty"`
	PendingTurn         int                  `json:"pendingTurn,omitempty"`
	LastRun             time.Time            `json:"lastRun,omitempty"`
	FailedProfiles      map[string]time.Time `json:"failedProfiles,omitempty"`
	SmellDay            string               `json:"smellDay,omitempty"`
	Smells              int                  `json:"smells,omitempty"`
	SmellSession        string               `json:"smellSession,omitempty"`
	SmellHead           string               `json:"smellHead,omitempty"`
	SmellPending        string               `json:"smellPending,omitempty"`
	SmellPendingTurn    int                  `json:"smellPendingTurn,omitempty"`
	SmellLastRun        time.Time            `json:"smellLastRun,omitempty"`
	SmellFailedProfiles map[string]time.Time `json:"smellFailedProfiles,omitempty"`
}

// Store keeps librarian state in dyna.db.
type Store struct{ DataDir string }

// Migrate brings the librarian tables in dataDir's dyna.db to this version.
// Only the engine calls it, at start (requirement 41).
func Migrate(dataDir string) error {
	_, err := Store{DataDir: dataDir}.db()
	return err
}

func (s Store) db() (*sql.DB, error) {
	db, err := database.Open(s.DataDir)
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "librarian.version", schemaVersion, func(conn *sql.Conn, old int) error {
		if old == 0 {
			_, err := conn.ExecContext(context.Background(), schema)
			return err
		}
		if old < 2 {
			_, err := conn.ExecContext(context.Background(), smellPassSchema)
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return db, nil
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func get(q rowQuerier, repo string) (State, error) {
	st := State{Repo: repo}
	var lastRun, failed, smellLastRun, smellFailed string
	err := q.QueryRowContext(context.Background(), `SELECT main, room_id, session, head, pending, pending_turn, last_run, failed_profiles, smell_day, smells, smell_session, smell_head, smell_pending, smell_pending_turn, smell_last_run, smell_failed_profiles FROM librarian_repos WHERE repo=?`, repo).
		Scan(&st.Main, &st.RoomID, &st.Session, &st.Head, &st.Pending, &st.PendingTurn, &lastRun, &failed, &st.SmellDay, &st.Smells, &st.SmellSession, &st.SmellHead, &st.SmellPending, &st.SmellPendingTurn, &smellLastRun, &smellFailed)
	if errors.Is(err, sql.ErrNoRows) {
		return st, fmt.Errorf("the librarian is not enabled for this repository: %w", os.ErrNotExist)
	}
	if err != nil {
		return st, err
	}
	if lastRun != "" {
		if st.LastRun, err = time.Parse(time.RFC3339Nano, lastRun); err != nil {
			return st, err
		}
	}
	if smellLastRun != "" {
		if st.SmellLastRun, err = time.Parse(time.RFC3339Nano, smellLastRun); err != nil {
			return st, err
		}
	}
	if err := json.Unmarshal([]byte(failed), &st.FailedProfiles); err != nil {
		return st, err
	}
	return st, json.Unmarshal([]byte(smellFailed), &st.SmellFailedProfiles)
}

// Get returns a repository's state. A repository that was never enabled
// wraps os.ErrNotExist.
func (s Store) Get(repo string) (State, error) {
	db, err := s.db()
	if err != nil {
		return State{Repo: repo}, err
	}
	return get(db, repo)
}

// Update changes one repository's state in a writer transaction. create
// allows a missing row, which fn then fills.
func (s Store) Update(repo string, create bool, fn func(*State) error) (State, error) {
	db, err := s.db()
	if err != nil {
		return State{}, err
	}
	var out State
	err = state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		st, err := get(conn, repo)
		if err != nil && !(create && errors.Is(err, os.ErrNotExist)) {
			return err
		}
		if err := fn(&st); err != nil {
			return err
		}
		failed, _ := json.Marshal(st.FailedProfiles)
		if st.FailedProfiles == nil {
			failed = []byte("{}")
		}
		smellFailed, _ := json.Marshal(st.SmellFailedProfiles)
		if st.SmellFailedProfiles == nil {
			smellFailed = []byte("{}")
		}
		formatTime := func(value time.Time) string {
			if value.IsZero() {
				return ""
			}
			return value.UTC().Format(time.RFC3339Nano)
		}
		_, err = conn.ExecContext(context.Background(), `INSERT OR REPLACE INTO librarian_repos(repo, main, room_id, session, head, pending, pending_turn, last_run, failed_profiles, smell_day, smells, smell_session, smell_head, smell_pending, smell_pending_turn, smell_last_run, smell_failed_profiles) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			repo, st.Main, st.RoomID, st.Session, st.Head, st.Pending, st.PendingTurn, formatTime(st.LastRun), string(failed), st.SmellDay, st.Smells, st.SmellSession, st.SmellHead, st.SmellPending, st.SmellPendingTurn, formatTime(st.SmellLastRun), string(smellFailed))
		out = st
		return err
	})
	return out, err
}

// RoomRepo returns the repository whose librarian a room holds, or "".
func (s Store) RoomRepo(roomID string) (string, error) {
	db, err := s.db()
	if err != nil {
		return "", err
	}
	var repo string
	err = db.QueryRowContext(context.Background(), `SELECT repo FROM librarian_repos WHERE room_id=? LIMIT 1`, roomID).Scan(&repo)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return repo, err
}
