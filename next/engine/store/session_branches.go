package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// ErrBranchTaken is a branch that another session that is not closed has on the repository.
var ErrBranchTaken = errors.New("another session has that branch")

// SessionBranches lists the branches recorded as a session's own on repo, newest first. The first is its branch.
func (s *Store) SessionBranches(ctx context.Context, repo contract.RepoName, session contract.SessionID) ([]string, error) {
	return listIDs[string](ctx, s.Read(), "SELECT branch FROM session_branches WHERE repo=? AND session=? ORDER BY created_at DESC,rowid DESC", repo, session)
}

// BranchHolder returns the session that is not closed and has branch recorded on repo, or "" when none has.
func (s *Store) BranchHolder(ctx context.Context, repo contract.RepoName, branch string) (contract.SessionID, error) {
	var holder contract.SessionID
	err := s.Read().QueryRowContext(ctx, "SELECT b.session FROM session_branches b JOIN sessions o ON o.id=b.session WHERE b.repo=? AND b.branch=? AND o.closed_at IS NULL", repo, branch).Scan(&holder)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return holder, err
}

const heldByAnother = "SELECT EXISTS(SELECT 1 FROM session_branches b JOIN sessions o ON o.id=b.session WHERE b.repo=? AND b.branch=? AND b.session!=? AND o.closed_at IS NULL)"

// ClaimBranch records the session's branch on repo and returns it: the one it already has, else the first of wanted,
// wanted-2, wanted-3 and on that is not in unavailable and that no other session that is not closed has.
func (s *Store) ClaimBranch(ctx context.Context, repo contract.RepoName, session contract.SessionID, wanted string, unavailable map[string]bool, at time.Time) (string, error) {
	var branch string
	err := s.Write(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, "SELECT branch FROM session_branches WHERE repo=? AND session=? ORDER BY created_at DESC,rowid DESC LIMIT 1", repo, session).Scan(&branch)
		if err == nil || !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		for n := 1; ; n++ {
			branch = wanted
			if n > 1 {
				branch = fmt.Sprintf("%s-%d", wanted, n)
			}
			var held bool
			if err := tx.QueryRowContext(ctx, heldByAnother, repo, branch, session).Scan(&held); err != nil {
				return err
			}
			if !held && !unavailable[branch] {
				_, err := tx.ExecContext(ctx, "INSERT INTO session_branches(repo,branch,session,created_at) VALUES(?,?,?,?)", repo, branch, session, at.UnixMilli())
				return err
			}
		}
	})
	return branch, err
}

// AddBranch records branch as the session's newest, so its branch from now on. Its earlier rows stay its own.
func (s *Store) AddBranch(ctx context.Context, repo contract.RepoName, session contract.SessionID, branch string, at time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var held bool
		if err := tx.QueryRowContext(ctx, heldByAnother, repo, branch, session).Scan(&held); err != nil {
			return err
		}
		if held {
			return ErrBranchTaken
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO session_branches(repo,branch,session,created_at) VALUES(?,?,?,?) ON CONFLICT(repo,branch,session) DO UPDATE SET created_at=excluded.created_at", repo, branch, session, at.UnixMilli())
		return err
	})
}
