package room

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
	ID      string     `json:"id"`
	Number  int        `json:"number"`
	Title   string     `json:"title"`
	File    string     `json:"file"`
	Line    int        `json:"line"`
	Filer   string     `json:"filer"`
	FiledAt int64      `json:"filedAt"`
	State   IssueState `json:"state"`
	Reason  string     `json:"reason"`
	GoalID  string     `json:"goalId"`
	// Assignee is the session the issue was given to, when triage gave it to a
	// goal holder.
	Assignee  string `json:"assignee,omitempty"`
	GitHubURL string `json:"githubUrl"`
	// FixPR is the open PR that closes this issue on merge. It is filled at
	// read time from the PR store, so it is never part of the stored issue.
	FixPR int `json:"fixPr,omitempty"`
	// InProgress, GoalShared and GoalHolder are computed at read time from
	// open goals and PR links. They are never part of the stored issue.
	InProgress bool   `json:"inProgress"`
	GoalShared bool   `json:"goalShared,omitempty"`
	GoalHolder string `json:"goalHolder,omitempty"`
	ChangedBy  string `json:"changedBy"`
	ChangedAt  int64  `json:"changedAt"`
}

// Label is the short issue reference people can type within its room.
func (i Issue) Label() string {
	if i.Number > 0 {
		return fmt.Sprintf("D-%d", i.Number)
	}
	return i.ID
}

// CloseOrigin names the PR merge that closes an issue automatically. Its zero
// value is a manual close by the owner or root. A non-zero origin makes the
// close the engine's own: the event names the engine, not the session whose
// authority the engine used.
type CloseOrigin struct {
	PR     int    `json:"pr,omitempty"`
	Commit string `json:"commit,omitempty"`
}

// CloseIssue closes an open issue with a reason. origin stays zero for a
// manual close and names the merging PR for an automatic one. The automatic
// path that fills origin is the tracked-PR close (goal g343, change
// fix/pr-closes-issues), which calls this from the merge watcher.
// The caller on ctx decides whether the close is allowed; by only credits it.
func (s *Store) CloseIssue(ctx context.Context, roomID, id, by, reason string, origin CloseOrigin) (Issue, error) {
	return s.changeIssueFrom(ctx, roomID, id, by, origin, true, "ChangeIssue", func(i *Issue) error {
		i.State = IssueStateClosed
		i.Reason = reason
		return nil
	})
}

// checkIssueState rejects a stored state the issue protocol does not define,
// so a typo cannot pass from storage to a view as a finite state.
func checkIssueState(i Issue) error {
	if i.State.Valid() {
		return nil
	}
	return fmt.Errorf("issue %s has unknown state %q", i.Label(), i.State)
}

// issueNumber accepts the canonical label and the compact composer spelling.
func issueNumber(ref string) (int, bool) {
	ref = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(ref)), "#")
	if !strings.HasPrefix(ref, "d") {
		return 0, false
	}
	ref = strings.TrimPrefix(strings.TrimPrefix(ref, "d"), "-")
	for _, digit := range ref {
		if digit < '0' || digit > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(ref)
	return n, err == nil && n > 0
}

// IsIssueNumber reports whether ref is an issue reference on its own, such as
// D-12, d12 or D12, and holds nothing else. A title written that way names an
// existing issue instead of filing a new one.
func IsIssueNumber(ref string) bool {
	_, ok := issueNumber(ref)
	return ok
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
	// A row from an older writer may lack a number; Label then falls back to
	// its message ID instead of hiding the issue.
	query := `SELECT i.body, COALESCE(n.number,0) FROM room_issues i LEFT JOIN room_issue_numbers n ON n.room_id=i.room_id AND n.message_id=i.id WHERE i.room_id=? AND i.state='open' ORDER BY i.seq DESC`
	if all {
		query = `SELECT i.body, COALESCE(n.number,0) FROM room_issues i LEFT JOIN room_issue_numbers n ON n.room_id=i.room_id AND n.message_id=i.id WHERE i.room_id=? ORDER BY i.seq DESC`
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
		var number int
		if err := rows.Scan(&body, &number); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(body), &i); err != nil {
			return nil, err
		}
		i.Number = number
		if err := checkIssueState(i); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// Migrate brings the room record, issue, pin, owner-read and post-index
// tables in dataDir's dyna.db to this version.
// Only the engine calls it, at start (requirement 41).
func Migrate(dataDir string) error {
	s := &Store{Root: filepath.Join(dataDir, "rooms")}
	if err := s.migrateRecords(); err != nil {
		return err
	}
	if err := s.migratePostIndex(); err != nil {
		return err
	}
	if _, err := s.issuesDB(); err != nil {
		return err
	}
	if _, err := s.pinsDB(); err != nil {
		return err
	}
	if _, err := s.ownerReadsDB(); err != nil {
		return err
	}
	_, err := s.postNormalizationsDB()
	return err
}

func (s *Store) issuesDB() (*sql.DB, error) {
	db, err := database.Open(filepath.Dir(s.root()))
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "room.issues.schema", 3, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		if old < 1 {
			if _, err := conn.ExecContext(ctx, issuesSchema); err != nil {
				return err
			}
		}
		if old < 2 {
			if err := s.importLegacyIssues(ctx, conn); err != nil {
				return err
			}
		}
		// Keep the mapping after triage. Each room has its own sequence, and
		// old posts get numbers in creation order, with seq breaking ties.
		_, err := conn.ExecContext(ctx, `CREATE TABLE room_issue_numbers (
 room_id TEXT NOT NULL, number INTEGER NOT NULL CHECK(number > 0), message_id TEXT NOT NULL,
 PRIMARY KEY(room_id, number), UNIQUE(room_id, message_id)
);
INSERT INTO room_issue_numbers(room_id, number, message_id)
 SELECT room_id, ROW_NUMBER() OVER (PARTITION BY room_id ORDER BY COALESCE(json_extract(body,'$.filedAt'),0), seq), id
 FROM room_issues;`)
		return err
	})
	if err != nil {
		return nil, err
	}
	return db, nil
}

// TODO: legacy/shim remove later — imports retained issues.json only during
// schema upgrade; remove once supported stores have passed room.issues.schema 2.
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
		id := entry.Name()
		path := filepath.Join(s.root(), id, IssuesFile)
		_, err := state.ImportMigration(ctx, conn, roomKey(id)+"/issues", path, func(conn *sql.Conn, raw []byte) error {
			var rows []Issue
			if err := json.Unmarshal(raw, &rows); err != nil {
				return fmt.Errorf("import issues %s: %w", path, err)
			}
			seen := map[string]bool{}
			var sequence int64
			// seq is a table-wide primary key. Recover below every current row.
			if err := conn.QueryRowContext(ctx, "SELECT COALESCE(MIN(seq),0) FROM room_issues").Scan(&sequence); err != nil {
				return err
			}
			// The source lists newest first. Descending sequence values keep that order.
			for _, issue := range rows {
				if seen[issue.ID] {
					return fmt.Errorf("import issues %s: duplicate issue ID %q", path, issue.ID)
				}
				seen[issue.ID] = true
				if err := checkIssueState(issue); err != nil {
					return fmt.Errorf("import issues %s: %w", path, err)
				}
				var exists bool
				if err := conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM room_issues WHERE room_id=? AND id=?)", id, issue.ID).Scan(&exists); err != nil {
					return err
				}
				if exists {
					continue
				}
				body, err := json.Marshal(issue)
				if err != nil {
					return err
				}
				sequence--
				if _, err := conn.ExecContext(ctx, "INSERT INTO room_issues(seq,room_id,id,state,body) VALUES(?,?,?,?,?)", sequence, id, issue.ID, issue.State, string(body)); err != nil {
					return err
				}
				if err := bumpIssues(ctx, conn, id); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func insertIssue(ctx context.Context, conn *sql.Conn, roomID string, i *Issue) error {
	if _, err := conn.ExecContext(ctx, `INSERT INTO room_issue_numbers(room_id,number,message_id) VALUES(?,?,?)`, roomID, i.Number, i.ID); err != nil {
		return err
	}
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
	if _, err := conn.ExecContext(ctx, `DELETE FROM room_issue_numbers WHERE room_id=?`, roomID); err != nil {
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

// IssueInput is what a filer knows when it files an issue. Note is the free
// text a person reads; Files, Symbols, Cost and Direction are the same facts
// in the typed shape the librarian's note carries.
type IssueInput struct {
	Title     string
	Note      string
	File      string
	Line      int
	Filer     string
	Files     []IssueFile
	Symbols   []string
	Cost      string
	Direction string
}

// FileIssue appends a quiet room post and records its initial open state. The
// post is an issue/filed event, so every filer gets the same card, and a new
// issue wakes nobody: it is backlog.
func (s *Store) FileIssue(roomID string, in IssueInput) (Issue, error) {
	return s.fileIssue(roomID, in, false)
}

// FileQuietIssue files an issue into roomID for a filer that may live in another
// room. A filed issue notifies nobody, so a session in another room can report a
// Dyna problem here without switching rooms (D-111). Every other write keeps the
// rule that its filer belongs to the room.
func (s *Store) FileQuietIssue(roomID string, in IssueInput) (Issue, error) {
	return s.fileIssue(roomID, in, true)
}

func (s *Store) fileIssue(roomID string, in IssueInput, otherRoomFiler bool) (Issue, error) {
	title, by := strings.TrimSpace(in.Title), strings.TrimSpace(in.Filer)
	if title == "" || strings.ContainsAny(title, "\r\n") || by == "" || in.Line < 0 {
		return Issue{}, errors.New("an issue needs a one-line title, a filer and a nonnegative line")
	}
	// A whole title that is only an issue number names an issue to read. This
	// mistake filed empty issues titled "D-61" until the CLI read them instead.
	if number, ok := issueNumber(title); ok {
		return Issue{}, fmt.Errorf("D-%d is an issue, not a title: read it with dyna issue show D-%d", number, number)
	}
	rec, err := s.Resolve(roomID)
	if err != nil {
		return Issue{}, err
	}
	if by != rec.Name && !strings.HasPrefix(by, rec.Name+":") {
		if !otherRoomFiler || !s.filerNamesRoom(by) {
			return Issue{}, errors.New("issue filer must belong to this room")
		}
	}
	note := strings.TrimSpace(in.Note)
	files := in.Files
	if len(files) == 0 && in.File != "" {
		files = []IssueFile{{Path: in.File, Line: in.Line}}
	}
	var out Issue
	// The number is allocated before the post, because the event carries it:
	// the card shows D-N without a second read. A filing takes the numbering
	// lock, not the issue lock: an export holds that one while `gh issue create`
	// runs, and filing must not wait for GitHub. The numbering lock covers the
	// post and the insert, so two filings cannot take the same number.
	unlockNumbers, err := s.lockIssueNumbers(rec.ID)
	if err != nil {
		return Issue{}, err
	}
	defer unlockNumbers()
	number, err := s.nextIssueNumber(rec.ID)
	if err != nil {
		return Issue{}, err
	}
	// The post comes first, so a crash before the insert leaves a post without
	// an issue row rather than an issue no one can see. A retry reads the same
	// number again, because nothing committed it, and its event key (filed and
	// open for that D-N) matches the stored post: the retry gets the earlier
	// post back instead of a second one.
	msg, err := s.PostEventInput(PostEventInput{
		RoomID: rec.ID,
		Event: IssueEvent{
			Type: IssueFiled, Number: number, Title: title, Files: files, Symbols: in.Symbols,
			Cost: strings.TrimSpace(in.Cost), Direction: strings.TrimSpace(in.Direction),
			State: IssueStateOpen, Filer: by, Detail: note,
		},
		To: []string{rec.Name},
	})
	if err != nil {
		return Issue{}, err
	}
	out = Issue{ID: msg.ID, Number: number, Title: title, File: in.File, Line: in.Line, Filer: by, FiledAt: msg.TS, State: IssueStateOpen}
	db, err := s.issuesDB()
	if err != nil {
		return Issue{}, err
	}
	err = state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		return insertIssue(context.Background(), conn, rec.ID, &out)
	})
	return out, err
}

// lockIssueNumbers serializes number allocation for one room. It is separate
// from withIssuesLock, which an export holds while GitHub answers.
func (s *Store) lockIssueNumbers(roomID string) (func(), error) {
	path, err := s.issuesPath(roomID)
	if err != nil {
		return nil, err
	}
	return store.Lock(path + ".numbers.lock")
}

// nextIssueNumber reads the number a new issue takes in its room.
func (s *Store) nextIssueNumber(roomID string) (int, error) {
	db, err := s.issuesDB()
	if err != nil {
		return 0, err
	}
	var number int
	err = db.QueryRow(`SELECT COALESCE(MAX(number),0)+1 FROM room_issue_numbers WHERE room_id=?`, roomID).Scan(&number)
	return number, err
}

// Issue reads one room issue by message ID or D-N label.
func (s *Store) Issue(roomID, id string) (Issue, error) {
	rows, err := s.Issues(roomID, true)
	if err != nil {
		return Issue{}, err
	}
	number, numbered := issueNumber(id)
	for _, i := range rows {
		if i.ID == id || numbered && i.Number == number {
			return i, nil
		}
	}
	return Issue{}, fmt.Errorf("issue %q not found", id)
}

// ChangeIssue serializes triage, including its side effect, so concurrent
// requests cannot create two goals or export the same open issue twice.
func (s *Store) ChangeIssue(ctx context.Context, roomID, id, by string, change func(*Issue) error) (Issue, error) {
	return s.ChangeIssueFrom(ctx, roomID, id, by, CloseOrigin{}, change)
}

// ChangeIssueFrom is ChangeIssue with the origin of the close. origin names the
// PR merge that closes an issue automatically; its zero value is a manual
// close. The room store posts the typed event after the change, so every path
// that changes an issue uses it.
func (s *Store) ChangeIssueFrom(ctx context.Context, roomID, id, by string, origin CloseOrigin, change func(*Issue) error) (Issue, error) {
	return s.changeIssueFrom(ctx, roomID, id, by, origin, false, "ChangeIssue", change)
}

// LinkIssueGoal records goal work without closing the issue. A non-empty from
// is the state the issue must still have: the check and the write happen in the
// same locked step, so two callers cannot both claim one issue. Relinking a
// legacy goal issue is allowed, so a finished goal can be replaced by new work.
func (s *Store) LinkIssueGoal(ctx context.Context, roomID, id, goalID, by string, from IssueState) (Issue, error) {
	if strings.TrimSpace(goalID) == "" {
		return Issue{}, errors.New("goal link needs a goal ID")
	}
	return s.changeIssueFrom(ctx, roomID, id, by, CloseOrigin{}, true, "LinkIssueGoal", func(i *Issue) error {
		if from != "" && i.State != from {
			return fmt.Errorf("issue %s is already %s", i.Label(), i.State)
		}
		i.State, i.GoalID, i.Assignee = IssueStateGoal, goalID, ""
		return nil
	})
}

// CheckIssueGoalLink checks link permission before a goal or session changes.
func (s *Store) CheckIssueGoalLink(ctx context.Context, roomID string) error {
	rec, err := s.Resolve(roomID)
	if err != nil {
		return err
	}
	actor, err := authority.ActorIn(ctx, rec.ID)
	if err != nil {
		return err
	}
	return authority.Check("LinkIssueGoal", actor, nil)
}

// RestoreIssueGoal restores a previous open or goal link after a later step
// of starting or editing work fails.
func (s *Store) RestoreIssueGoal(ctx context.Context, roomID, id string, snapshot Issue, by string) (Issue, error) {
	if snapshot.State != IssueStateOpen && snapshot.State != IssueStateGoal {
		return Issue{}, errors.New("can restore only an open or goal issue")
	}
	return s.changeIssueFrom(ctx, roomID, id, by, CloseOrigin{}, true, "LinkIssueGoal", func(i *Issue) error {
		i.State, i.GoalID, i.Assignee = snapshot.State, snapshot.GoalID, snapshot.Assignee
		return nil
	})
}

// changeIssueFrom writes one issue change for the caller on ctx. The caller's
// verified role decides what the change may do; by is the address that credits
// it, so display conventions never grant authority.
func (s *Store) changeIssueFrom(ctx context.Context, roomID, id, by string, origin CloseOrigin, allowGoal bool, action string, change func(*Issue) error) (Issue, error) {
	rec, err := s.Resolve(roomID)
	if err != nil {
		return Issue{}, err
	}
	actor, err := authority.ActorIn(ctx, rec.ID)
	if err != nil {
		return Issue{}, err
	}
	if err := authority.Check(action, actor, nil); err != nil {
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
			number, numbered := issueNumber(id)
			if i.ID != id && (!numbered || i.Number != number) {
				continue
			}
			if i.State != IssueStateOpen && !(allowGoal && i.State == IssueStateGoal) {
				return fmt.Errorf("issue %s is already %s", i.Label(), i.State)
			}
			if err = change(&i); err != nil {
				return err
			}
			switch i.State {
			case IssueStateOpen:
				if action != "LinkIssueGoal" {
					return errors.New("invalid issue transition")
				}
			case IssueStateClosed:
				if strings.TrimSpace(i.Reason) == "" {
					return errors.New("closing an issue needs a reason")
				}
			case IssueStateGoal:
				if i.GoalID == "" {
					return errors.New("goal triage needs a goal ID")
				}
			case IssueStateExported:
				if i.GitHubURL == "" {
					return errors.New("export needs a GitHub issue URL")
				}
			default:
				return errors.New("invalid issue transition")
			}
			i.ChangedBy, i.ChangedAt = by, s.now().UnixMilli()
			body, err := json.Marshal(i)
			if err != nil {
				return err
			}
			// The change is committed before the event is posted, so a post
			// failure reports an error that a retry cannot repeat: the issue
			// is already closed. The event is a quiet feed row, and the next
			// change posts again.
			if err := state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
				ctx := context.Background()
				if _, err := conn.ExecContext(ctx, `UPDATE room_issues SET state=?, body=? WHERE room_id=? AND id=?`, i.State, string(body), rec.ID, i.ID); err != nil {
					return err
				}
				return bumpIssues(ctx, conn, rec.ID)
			}); err != nil {
				return err
			}
			// A populated result proves the write committed. Callers can then
			// distinguish an event failure from a pre-write failure.
			out = i
			return s.postIssueChange(rec, &i, by, origin)
		}
		return fmt.Errorf("issue %q not found", id)
	})
	return out, err
}

// postIssueChange posts the typed event of one issue change. It is quiet: the
// owner feed and an assignee read it, and no session wakes.
func (s *Store) postIssueChange(rec Record, i *Issue, by string, origin CloseOrigin) error {
	state := i.State
	change := IssueUpdated
	if state == IssueStateClosed {
		change = IssueClosed
	}
	ev := IssueEvent{
		Type: change, IssueID: i.ID, Number: i.Number, Title: i.Title, State: state,
		Filer: i.Filer, Actor: IssueActor(by), Assignee: i.Assignee,
	}
	if i.File != "" {
		ev.Files = []IssueFile{{Path: i.File, Line: i.Line}}
	}
	if change == IssueClosed {
		// The reason is one line, and the close template already names it. A
		// Detail block would repeat it in the agent text.
		ev.Reason = i.Reason
		if origin != (CloseOrigin{}) {
			// The engine closed the issue because a merged PR fixed it, so the
			// close names the engine. The card reads the PR and the commit.
			ev.Actor = ActorEngine
			ev.ClosedBy = &origin
		}
	}
	to := []string{rec.Name}
	if i.Assignee != "" && i.Assignee != rec.Name {
		to = append(to, i.Assignee)
	}
	_, err := s.PostEventInput(PostEventInput{RoomID: rec.ID, Event: ev, To: to})
	return err
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

// filerNamesRoom reports whether a filer address names an existing room,
// optionally with a member of it, such as "fl-rea:merge-captain".
func (s *Store) filerNamesRoom(by string) bool {
	name, _, _ := strings.Cut(by, ":")
	if strings.TrimSpace(name) == "" {
		return false
	}
	_, err := s.Resolve(name)
	return err == nil
}
