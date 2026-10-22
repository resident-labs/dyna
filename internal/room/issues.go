package room

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

// IssuesFile held issue state before dyna.db; it is read once at import, and
// its lock file still serializes triage side effects.
const IssuesFile = "issues.json"

// Issues live in dyna.db. seq keeps filing order, so newest comes first.
// room_issue_revisions changes on every write, so watchers see new state.
const issuesSchema = `CREATE TABLE room_issues (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 room_id TEXT NOT NULL, id TEXT NOT NULL, state TEXT NOT NULL,
 body TEXT NOT NULL CHECK(json_valid(body)),
 UNIQUE(room_id, id)
);
CREATE INDEX room_issues_state ON room_issues(room_id, state, seq);
CREATE TABLE room_issue_revisions (room_id TEXT PRIMARY KEY, revision INTEGER NOT NULL);`

type Issue struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	Filer     string `json:"filer"`
	FiledAt   int64  `json:"filedAt"`
	State     string `json:"state"`
	Reason    string `json:"reason"`
	GoalID    string `json:"goalId"`
	GitHubURL string `json:"githubUrl"`
	ChangedBy string `json:"changedBy"`
	ChangedAt int64  `json:"changedAt"`
}

func (s *Store) issuesPath(roomID string) (string, error) {
	dir, err := s.dir(roomID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, IssuesFile), nil
}

// Issues returns newest first. The default view contains only open issues.
func (s *Store) Issues(roomID string, all bool) ([]Issue, error) {
	if _, err := s.dir(roomID); err != nil {
		return nil, err
	}
	db, err := s.issuesDB()
	if err != nil {
		return nil, err
	}
	query := `SELECT body FROM room_issues WHERE room_id=? AND state='open' ORDER BY seq DESC`
	if all {
		query = `SELECT body FROM room_issues WHERE room_id=? ORDER BY seq DESC`
	}
	rows, err := db.Query(query, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Issue{}
	for rows.Next() {
		var body string
		var i Issue
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(body), &i); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// Migrate brings the room record, issue, pin and owner-read tables in
// dataDir's dyna.db to this version.
// Only the engine calls it, at start (requirement 41).
func Migrate(dataDir string) error {
	s := &Store{Root: filepath.Join(dataDir, "rooms")}
	if _, err := s.issuesDB(); err != nil {
		return err
	}
	if _, err := s.pinsDB(); err != nil {
		return err
	}
	_, err := s.ownerReadsDB()
	return err
}

func (s *Store) issuesDB() (*sql.DB, error) {
	db, err := database.Open(filepath.Dir(s.root()))
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "room.issues.schema", 1, func(conn *sql.Conn, _ int) error {
		ctx := context.Background()
		if _, err := conn.ExecContext(ctx, issuesSchema); err != nil {
			return err
		}
		return s.importLegacyIssues(ctx, conn)
	})
	if err != nil {
		return nil, err
	}
	return db, nil
}

// TODO: legacy/shim remove later — copies each room's issues.json into dyna.db once and leaves the file in place so an older binary can roll back; remove with the files after every host has run a SQLite build for a while.
func (s *Store) importLegacyIssues(ctx context.Context, conn *sql.Conn) error {
	entries, err := os.ReadDir(s.root())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !ValidID(entry.Name()) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.root(), entry.Name(), IssuesFile))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		var rows []Issue
		if err := json.Unmarshal(b, &rows); err != nil {
			// Fail the whole import, as the old reader failed: the version
			// stays unrecorded, so repairing the file lets the next call retry.
			return fmt.Errorf("import %s: %w", filepath.Join(s.root(), entry.Name(), IssuesFile), err)
		}
		// The file lists newest first; insert oldest first so seq keeps the order.
		for n := len(rows) - 1; n >= 0; n-- {
			if err := insertIssue(ctx, conn, entry.Name(), rows[n]); err != nil {
				return err
			}
		}
	}
	return nil
}

func insertIssue(ctx context.Context, conn *sql.Conn, roomID string, i Issue) error {
	body, err := json.Marshal(i)
	if err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO room_issues(room_id,id,state,body) VALUES(?,?,?,?)`, roomID, i.ID, i.State, string(body)); err != nil {
		return err
	}
	return bumpIssues(ctx, conn, roomID)
}

func bumpIssues(ctx context.Context, conn *sql.Conn, roomID string) error {
	_, err := conn.ExecContext(ctx, `INSERT INTO room_issue_revisions(room_id,revision) VALUES(?,1) ON CONFLICT(room_id) DO UPDATE SET revision=revision+1`, roomID)
	return err
}

// deleteIssueRows removes a deleted room's issues, in the delete's write.
func deleteIssueRows(ctx context.Context, conn *sql.Conn, roomID string) error {
	if _, err := conn.ExecContext(ctx, `DELETE FROM room_issues WHERE room_id=?`, roomID); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, `DELETE FROM room_issue_revisions WHERE room_id=?`, roomID)
	return err
}

// IssuesRevision changes whenever the room's issues change.
func (s *Store) IssuesRevision(roomID string) (int64, error) {
	db, err := s.issuesDB()
	if err != nil {
		return 0, err
	}
	var rev int64
	err = db.QueryRow(`SELECT revision FROM room_issue_revisions WHERE room_id=?`, roomID).Scan(&rev)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return rev, err
}

// FileIssue appends a quiet room post and records its initial open state.
func (s *Store) FileIssue(roomID, title, note, file string, line int, by string) (Issue, error) {
	title, by = strings.TrimSpace(title), strings.TrimSpace(by)
	if title == "" || strings.ContainsAny(title, "\r\n") || by == "" || line < 0 {
		return Issue{}, errors.New("an issue needs a one-line title, a filer and a nonnegative line")
	}
	rec, err := s.Resolve(roomID)
	if err != nil {
		return Issue{}, err
	}
	if by != rec.Name && !strings.HasPrefix(by, rec.Name+":") {
		return Issue{}, errors.New("issue filer must belong to this room")
	}
	text := title
	if note = strings.TrimSpace(note); note != "" {
		text += "\n\n" + note
	}
	msg, err := s.Post(roomID, PostInput{From: by, To: []string{rec.Name}, Kind: "issue", Text: text})
	if err != nil {
		return Issue{}, err
	}
	out := Issue{ID: msg.ID, Title: title, File: file, Line: line, Filer: by, FiledAt: msg.TS, State: "open"}
	db, err := s.issuesDB()
	if err != nil {
		return Issue{}, err
	}
	err = state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		return insertIssue(context.Background(), conn, rec.ID, out)
	})
	return out, err
}

// ChangeIssue serializes triage, including its side effect, so concurrent
// requests cannot create two goals or export the same open issue twice.
func (s *Store) ChangeIssue(roomID, id, by string, change func(*Issue) error) (Issue, error) {
	rec, err := s.Resolve(roomID)
	if err != nil {
		return Issue{}, err
	}
	actor := authority.Actor{Role: authority.Session, Session: by, Room: rec.ID}
	switch by {
	case rec.Name:
		actor.Role = authority.Owner
	case rec.Name + ":" + RootAgent:
		actor.Role = authority.Root
	}
	if err := authority.Check("ChangeIssue", actor, nil); err != nil {
		return Issue{}, err
	}
	db, err := s.issuesDB()
	if err != nil {
		return Issue{}, err
	}
	var out Issue
	// The file lock serializes the side effect in change, which may write
	// other stores or wait for GitHub, so no database transaction is open
	// while it runs. Reads and the final write each use their own.
	err = s.withIssuesLock(roomID, func() error {
		rows, err := s.Issues(roomID, true)
		if err != nil {
			return err
		}
		for _, i := range rows {
			if i.ID != id {
				continue
			}
			if i.State != "open" {
				return fmt.Errorf("issue %s is already %s", id, i.State)
			}
			if err = change(&i); err != nil {
				return err
			}
			switch i.State {
			case "closed":
				if strings.TrimSpace(i.Reason) == "" {
					return errors.New("closing an issue needs a reason")
				}
			case "goal":
				if i.GoalID == "" {
					return errors.New("goal triage needs a goal ID")
				}
			case "exported":
				if i.GitHubURL == "" {
					return errors.New("export needs a GitHub issue URL")
				}
			default:
				return errors.New("invalid issue transition")
			}
			i.ChangedBy, i.ChangedAt = by, s.now().UnixMilli()
			out = i
			body, err := json.Marshal(i)
			if err != nil {
				return err
			}
			return state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
				ctx := context.Background()
				if _, err := conn.ExecContext(ctx, `UPDATE room_issues SET state=?, body=? WHERE room_id=? AND id=?`, i.State, string(body), rec.ID, i.ID); err != nil {
					return err
				}
				return bumpIssues(ctx, conn, rec.ID)
			})
		}
		return fmt.Errorf("issue %q not found", id)
	})
	return out, err
}

// Export can wait for GitHub. Its lock must not delay room posts.
func (s *Store) withIssuesLock(roomID string, fn func() error) error {
	path, err := s.issuesPath(roomID)
	if err != nil {
		return err
	}
	unlock, err := store.Lock(path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}
