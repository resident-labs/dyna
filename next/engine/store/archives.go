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

func (s *Store) SaveArchive(ctx context.Context, archive Archive) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO archives(session,repo,ref,commit_sha,created_at,expires_at) VALUES(?,?,?,?,?,?) ON CONFLICT(session,repo) DO UPDATE SET ref=excluded.ref,commit_sha=excluded.commit_sha,created_at=excluded.created_at,expires_at=excluded.expires_at", archive.Session, archive.Repo, archive.Ref, archive.Commit, archive.CreatedAt.UnixMilli(), archive.ExpiresAt.UnixMilli())
		return err
	})
}

func (s *Store) Archives(ctx context.Context, session contract.SessionID) ([]Archive, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT repo,ref,commit_sha,created_at,expires_at FROM archives WHERE session=? ORDER BY repo", session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var archives []Archive
	for rows.Next() {
		var a Archive
		var created, expires int64
		a.Session = session
		if err := rows.Scan(&a.Repo, &a.Ref, &a.Commit, &created, &expires); err != nil {
			return nil, err
		}
		a.CreatedAt, a.ExpiresAt = time.UnixMilli(created), time.UnixMilli(expires)
		archives = append(archives, a)
	}
	return archives, rows.Err()
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
