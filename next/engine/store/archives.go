package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/resident-labs/dyna/contract"
)

type Archive struct {
	Session   contract.SessionID
	Repo      contract.RepoName
	Ref       string
	Commit    string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// SaveArchives stores a complete set of verified reports atomically.
func (s *Store) SaveArchives(ctx context.Context, archives []Archive) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		for _, a := range archives {
			_, err := tx.ExecContext(ctx, "INSERT INTO archives(session,repo,ref,commit_sha,created_at,expires_at) VALUES(?,?,?,?,?,?) ON CONFLICT(session,repo) DO UPDATE SET ref=excluded.ref,commit_sha=excluded.commit_sha,created_at=excluded.created_at,expires_at=excluded.expires_at", a.Session, a.Repo, a.Ref, a.Commit, a.CreatedAt.UnixMilli(), a.ExpiresAt.UnixMilli())
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// SetClosingReason records an archive failure without deleting the box.
func (s *Store) SetClosingReason(ctx context.Context, id contract.BoxID, reason string, event contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE boxes SET reason=?,updated_at=? WHERE id=? AND state=?", reason, event.Time.UnixMilli(), id, contract.BoxClosing)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return sql.ErrNoRows
		}
		_, err = s.AppendEvent(tx, event)
		return err
	})
}
