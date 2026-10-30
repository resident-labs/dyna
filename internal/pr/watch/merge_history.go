package watch

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"path/filepath"
	"strings"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/pr/state"
	appstate "dyna-agent/internal/state"
)

const (
	mergeHistoryKept = 500 // rows kept for each repository
	mergeHistoryRead = 50  // rows returned for each room
)

const mergeHistorySchema = `CREATE TABLE IF NOT EXISTS merge_history (
 id INTEGER PRIMARY KEY AUTOINCREMENT, repo TEXT NOT NULL, rooms TEXT NOT NULL,
 kind TEXT NOT NULL, outcome TEXT NOT NULL, branch TEXT NOT NULL, head TEXT NOT NULL,
 body TEXT NOT NULL CHECK(json_valid(body)), at INTEGER NOT NULL,
 UNIQUE(kind, outcome, branch, head)
);
CREATE INDEX IF NOT EXISTS merge_history_repo ON merge_history(repo, id);`

// Migrate brings the merge history tables in dataDir's dyna.db to this version.
// Only the engine calls it, at start (requirement 41).
func Migrate(dataDir string) error {
	_, err := (&Service{Store: &Store{Root: state.Root(dataDir)}}).mergeHistoryDB()
	return err
}

func (s *Service) mergeHistoryDB() (*sql.DB, error) {
	db, err := database.Open(filepath.Dir(s.Store.Root))
	if err != nil {
		return nil, err
	}
	return db, database.Migrate(db, "pr.merge_history", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), mergeHistorySchema)
		return err
	})
}

// recordMergeHistory saves one row. A repeated step of the same trial saves
// nothing, so retried ticks cannot duplicate history.
func (s *Service) recordMergeHistory(st State, repo string, b state.MergeTrial, kind, outcome, head, logs string) error {
	db, err := s.mergeHistoryDB()
	if err != nil {
		return err
	}
	entry := MergeHistoryEntry{Kind: kind, Outcome: outcome, Repo: repo, Branch: b.Branch, Head: head, Changes: []string{}, Detail: failureDetail(logs), At: s.now()}
	rooms := map[string]bool{}
	for _, change := range b.Changes {
		label := change.Key
		if r := st.Records[change.Key]; r != nil {
			label = changeLabel(*r)
			rooms[r.RoomID] = true
		}
		entry.Changes = append(entry.Changes, label)
	}
	var ids []string
	for id := range rooms {
		ids = append(ids, id)
	}
	return s.saveMergeHistory(db, entry, ids)
}

func (s *Service) saveMergeHistory(db *sql.DB, entry MergeHistoryEntry, ids []string) error {
	roomsJSON, _ := json.Marshal(ids)
	body, _ := json.Marshal(entry)
	ctx := context.Background()
	return appstate.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO merge_history(repo, rooms, kind, outcome, branch, head, body, at) VALUES(?,?,?,?,?,?,?,?)`,
			entry.Repo, string(roomsJSON), entry.Kind, entry.Outcome, entry.Branch, entry.Head, string(body), entry.At.UnixNano()); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, `DELETE FROM merge_history WHERE repo = ? AND id NOT IN (SELECT id FROM merge_history WHERE repo = ? ORDER BY id DESC LIMIT ?)`, entry.Repo, entry.Repo, mergeHistoryKept)
		return err
	})
}

// noteMergeHistory keeps history out of the merge path: a failed write is logged.
func (s *Service) noteMergeHistory(st State, repo string, b state.MergeTrial, kind, outcome, head, logs string) {
	if err := s.recordMergeHistory(st, repo, b, kind, outcome, head, logs); err != nil {
		log.Printf("merge history for %s: %v", repo, err)
	}
}

// RoomMergeHistory returns the room's newest trial results and publishes first.
func (s *Service) RoomMergeHistory(roomID string) ([]MergeHistoryEntry, error) {
	db, err := s.mergeHistoryDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT body FROM merge_history WHERE EXISTS (SELECT 1 FROM json_each(rooms) WHERE value = ?) ORDER BY id DESC LIMIT ?`, roomID, mergeHistoryRead)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MergeHistoryEntry{}
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var entry MergeHistoryEntry
		if err := json.Unmarshal([]byte(body), &entry); err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

// failureDetail names what failed: the local suite with its first line, or the
// first log line for GitHub CI.
func failureDetail(logs string) string {
	if strings.TrimSpace(logs) == "" {
		return ""
	}
	suite, excerpt := returnedLocalFailure(logs)
	line := excerpt
	for _, candidate := range strings.Split(excerpt, "\n") {
		if strings.TrimSpace(candidate) != "" {
			line = strings.TrimSpace(candidate)
			break
		}
	}
	if suite != "" && !strings.HasPrefix(line, suite) {
		line = suite + ": " + line
	}
	return clip(line, 300)
}
