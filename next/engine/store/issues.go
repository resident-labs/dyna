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

// ItemMeta is what a pass keeps about a PR or an issue beyond its summary, all of it GitHub's latest as the feeds show it.
// Body is a hash of the text, so an edit shows without keeping the text. EditedBy is the last outside login to set free text
// on the item, which then reads as theirs. Waiting holds what the wake limits of spec 7.7 keep back, so it survives a restart.
// Recheck means GitHub had not worked out mergeability on the last read, and HeadAt is when that read saw a new head, in Unix
// milliseconds.
type ItemMeta struct {
	CancelledChecks []string              `json:"cancelledChecks,omitempty"`
	Labels          []string              `json:"labels,omitempty"`
	Assignees       []string              `json:"assignees,omitempty"`
	Milestone       string                `json:"milestone,omitempty"`
	Body            string                `json:"body,omitempty"`
	EditedBy        string                `json:"editedBy,omitempty"`
	Waiting         []contract.GitHubItem `json:"waiting,omitempty"`
	Recheck         bool                  `json:"recheck,omitempty"`
	HeadAt          int64                 `json:"headAt,omitempty"`
}

func decodeMeta(text string) (ItemMeta, error) {
	var meta ItemMeta
	if text == "" {
		return meta, nil
	}
	return meta, json.Unmarshal([]byte(text), &meta)
}

func encodeMeta(meta ItemMeta) (string, error) {
	data, err := json.Marshal(meta)
	return string(data), err
}

// TrackedIssue is one issue_owners row. Baseline means the next pass records the issue without a wake. OwnWakeAt and
// MetaWakeAt are when the token's own comments and a metadata change last woke a session, in Unix milliseconds.
type TrackedIssue struct {
	contract.IssueSummary
	Room       contract.RoomID
	Baseline   bool
	OwnWakeAt  int64
	Meta       ItemMeta
	MetaWakeAt int64
}

// IssueUpdate is a pass's result for one issue, read while the row's state was Was. A nil Change writes the row and tells nobody.
type IssueUpdate struct {
	Issue      contract.IssueSummary
	Was        contract.IssueState
	OwnWakeAt  int64
	Meta       ItemMeta
	MetaWakeAt int64
	Change     *contract.IssueChange
}

const issueColumns = `i.repo,i.number,i.url,i.title,i.author,i.session,i.state,s.room,i.baseline,i.own_wake_at,i.meta,i.meta_wake_at FROM issue_owners i JOIN sessions s ON s.id=i.session`

func scanIssue(row interface{ Scan(...any) error }) (TrackedIssue, error) {
	var i TrackedIssue
	var meta string
	if err := row.Scan(&i.Repo, &i.Number, &i.URL, &i.Title, &i.Author, &i.Session, &i.State, &i.Room, &i.Baseline, &i.OwnWakeAt, &meta, &i.MetaWakeAt); err != nil {
		return i, err
	}
	var err error
	i.Meta, err = decodeMeta(meta)
	i.Labels, i.Assignees, i.Milestone = i.Meta.Labels, i.Meta.Assignees, i.Meta.Milestone
	return i, err
}

func (s *Store) queryIssues(ctx context.Context, query string, args ...any) ([]TrackedIssue, error) {
	return queryAll(ctx, s.Read(), scanIssue, query, args...)
}

// TrackedIssues lists every tracked issue. A closed one is read no more, but a feed can still reopen it.
func (s *Store) TrackedIssues(ctx context.Context) ([]TrackedIssue, error) {
	return s.queryIssues(ctx, `SELECT `+issueColumns+` ORDER BY i.repo,i.number`)
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
	meta, err := encodeMeta(issue.Meta)
	if err != nil {
		return err
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `INSERT INTO issue_owners(repo,number,session,url,title,author,state,meta) VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(repo,number) DO UPDATE SET session=excluded.session,url=excluded.url,title=excluded.title,author=excluded.author,state=excluded.state,
own_wake_at=0,meta=excluded.meta,meta_wake_at=0,baseline=1 WHERE state<>'open'`,
			issue.Repo, issue.Number, issue.Session, issue.URL, issue.Title, issue.Author, issue.State, meta)
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
