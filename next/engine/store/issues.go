package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/resident-labs/dyna/contract"
)

// ErrIssueTracked means another session already tracks this open issue.
var ErrIssueTracked = errors.New("another session tracks this issue")

// ItemMeta is a PR's or an issue's metadata as last told to its session, so a change waiting out the wake window stays a change.
// Reopened counts GitHub's reopen events, and Draft and Reviewers belong to PRs. A row holds none until a poll has read it.
type ItemMeta struct {
	Title     string   `json:"title"`
	Labels    []string `json:"labels,omitempty"`
	Assignees []string `json:"assignees,omitempty"`
	Milestone string   `json:"milestone,omitempty"`
	EditedAt  string   `json:"editedAt,omitempty"`
	Reopened  int      `json:"reopened,omitempty"`
	Draft     bool     `json:"draft,omitempty"`
	Reviewers []string `json:"reviewers,omitempty"`
}

func decodeMeta(text string) (*ItemMeta, error) {
	if text == "" {
		return nil, nil
	}
	var meta ItemMeta
	if err := json.Unmarshal([]byte(text), &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func encodeMeta(meta *ItemMeta) (string, error) {
	if meta == nil {
		return "", nil
	}
	data, err := json.Marshal(meta)
	return string(data), err
}

// TrackedIssue is one issue_owners row. Baseline means the next poll records the issue without a wake. OwnWakeAt and
// MetaWakeAt are when the token's own comments and a metadata change last woke a session, in Unix milliseconds.
type TrackedIssue struct {
	contract.IssueSummary
	Room         contract.RoomID
	Baseline     bool
	SeenComments int64
	OwnWakeAt    int64
	Meta         *ItemMeta
	MetaWakeAt   int64
}

// IssueUpdate is a poll's result for one issue. A nil Change writes the row and tells nobody.
type IssueUpdate struct {
	Issue        contract.IssueSummary
	SeenComments int64
	OwnWakeAt    int64
	Meta         *ItemMeta
	MetaWakeAt   int64
	Change       *contract.IssueChange
}

const issueColumns = `i.repo,i.number,i.url,i.title,i.author,i.session,i.state,s.room,i.baseline,i.seen_comments,i.own_wake_at,i.meta,i.meta_wake_at FROM issue_owners i JOIN sessions s ON s.id=i.session`

func scanIssue(row interface{ Scan(...any) error }) (TrackedIssue, error) {
	var i TrackedIssue
	var meta string
	if err := row.Scan(&i.Repo, &i.Number, &i.URL, &i.Title, &i.Author, &i.Session, &i.State, &i.Room, &i.Baseline, &i.SeenComments, &i.OwnWakeAt, &meta, &i.MetaWakeAt); err != nil {
		return i, err
	}
	var err error
	if i.Meta, err = decodeMeta(meta); err == nil && i.Meta != nil {
		i.Labels, i.Assignees, i.Milestone = i.Meta.Labels, i.Meta.Assignees, i.Meta.Milestone
	}
	return i, err
}

func (s *Store) queryIssues(ctx context.Context, query string, args ...any) ([]TrackedIssue, error) {
	rows, err := s.Read().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrackedIssue
	for rows.Next() {
		i, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// TrackedIssues lists every issue the poll reads.
func (s *Store) TrackedIssues(ctx context.Context) ([]TrackedIssue, error) {
	return s.queryIssues(ctx, `SELECT `+issueColumns+` WHERE i.state='open' ORDER BY i.repo,i.number`)
}

// RoomIssues lists a room's issues, or every room's when room is empty. all adds closed ones, newest numbers first.
func (s *Store) RoomIssues(ctx context.Context, room contract.RoomID, all bool) ([]TrackedIssue, error) {
	return s.queryIssues(ctx, `SELECT `+issueColumns+` WHERE (?1='' OR s.room=?1) AND (?2 OR i.state='open') ORDER BY i.state='open' DESC,i.repo,i.number DESC LIMIT 500`, room, all)
}

// Issue reads one tracked issue.
func (s *Store) Issue(ctx context.Context, repo contract.RepoName, number int) (TrackedIssue, error) {
	return scanIssue(s.Read().QueryRowContext(ctx, `SELECT `+issueColumns+` WHERE i.repo=? AND i.number=?`, repo, number))
}

// TrackIssue adds an issue with a baseline, so what it already shows wakes nobody. A closed row can be tracked again.
func (s *Store) TrackIssue(ctx context.Context, issue TrackedIssue) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `INSERT INTO issue_owners(repo,number,session,url,title,author,state) VALUES(?,?,?,?,?,?,?)
ON CONFLICT(repo,number) DO UPDATE SET session=excluded.session,url=excluded.url,title=excluded.title,author=excluded.author,state=excluded.state,
seen_comments=0,own_wake_at=0,meta='',meta_wake_at=0,baseline=1 WHERE state<>'open'`,
			issue.Repo, issue.Number, issue.Session, issue.URL, issue.Title, issue.Author, issue.State)
		if err != nil {
			return err
		}
		return s.refuseOtherOwner(ctx, tx, result, "issue_owners", issue.Repo, issue.Number, issue.Session, ErrIssueTracked)
	})
}

// UntrackIssue stops tracking an issue.
func (s *Store) UntrackIssue(ctx context.Context, repo contract.RepoName, number int) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM issue_owners WHERE repo=? AND number=?", repo, number)
		return err
	})
}

// ContextIssue is one tracked open issue. It holds nothing from GitHub's free text, so the snapshot needs no wrapper.
type ContextIssue struct {
	Repo   contract.RepoName
	Number int
}

func (s *Store) ContextIssues(ctx context.Context, id contract.SessionID) ([]ContextIssue, error) {
	rows, err := s.Read().QueryContext(ctx, `SELECT repo,number FROM issue_owners WHERE session=? AND state='open' ORDER BY repo,number`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ContextIssue
	for rows.Next() {
		var i ContextIssue
		if err := rows.Scan(&i.Repo, &i.Number); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}
