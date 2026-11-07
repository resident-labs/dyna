package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/resident-labs/dyna/contract"
)

// CheckPRStack checks durable ownership, refusing ambiguous case-insensitive identities.
func (s *Store) CheckPRStack(ctx context.Context, repo contract.RepoName, prs []int, caller contract.SessionID, room contract.RoomID) error {
	return checkPRStack(ctx, s.Read(), repo, prs, caller, room)
}
func checkPRStack(ctx context.Context, q sqlQuery, repo contract.RepoName, prs []int, caller contract.SessionID, room contract.RoomID) error {
	tree, err := loadTree(ctx, q, room)
	if err != nil {
		return err
	}
	for _, n := range prs {
		var count int
		var owner contract.SessionID
		var available bool
		err = q.QueryRowContext(ctx, `SELECT count(*),coalesce(min(p.session),''),coalesce(min(p.state='open' AND p.stack IS NULL AND s.closed_at IS NULL AND s.state!='closing' AND s.room=?),0) FROM pr_owners p JOIN sessions s ON s.id=p.session WHERE p.repo=? COLLATE NOCASE AND p.number=?`, room, repo, n).Scan(&count, &owner, &available)
		if err != nil {
			return err
		}
		if count != 1 || !available || !(owner == caller || tree.Above(caller, owner)) {
			return fmt.Errorf("PR #%d must have one unambiguous, open, unstacked owner that is you or your open descendant", n)
		}
	}
	return nil
}

// RecordPRStack records all layers atomically without replacing ownership or losing tracking.
func (s *Store) RecordPRStack(ctx context.Context, repo contract.RepoName, prs []int, caller contract.SessionID, room contract.RoomID, stack int) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if err := checkPRStack(ctx, tx, repo, prs, caller, room); err != nil {
			return err
		}
		for i, n := range prs {
			result, err := tx.ExecContext(ctx, `UPDATE pr_owners SET stack=?,position=? WHERE repo=? COLLATE NOCASE AND number=? AND stack IS NULL`, stack, i+1, repo, n)
			if err != nil {
				return err
			}
			rows, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if rows != 1 {
				return fmt.Errorf("PR #%d changed before recording", n)
			}
		}
		return nil
	})
}
