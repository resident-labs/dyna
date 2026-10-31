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

// ExpiredArchives bounds each maintenance pass and preserves the oldest first.
func (s *Store) ExpiredArchives(ctx context.Context, now time.Time) ([]Archive, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT session,repo,ref,commit_sha,expires_at FROM archives WHERE expires_at<=? ORDER BY expires_at,session,repo LIMIT 50", now.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var archives []Archive
	for rows.Next() {
		var a Archive
		var expires int64
		if err := rows.Scan(&a.Session, &a.Repo, &a.Ref, &a.Commit, &expires); err != nil {
			return nil, err
		}
		a.ExpiresAt = time.UnixMilli(expires)
		archives = append(archives, a)
	}
	return archives, rows.Err()
}

// ForgetArchive removes only the exact report that was deleted remotely.
func (s *Store) ForgetArchive(ctx context.Context, a Archive) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM archives WHERE session=? AND repo=? AND ref=? AND commit_sha=? AND expires_at=?", a.Session, a.Repo, a.Ref, a.Commit, a.ExpiresAt.UnixMilli())
		return err
	})
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

// CompleteArchives stores verified reports and requests box removal atomically.
// An empty report set completes a close for a session without a repository.
func (s *Store) CompleteArchives(ctx context.Context, session contract.SessionID, archives []Archive) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE boxes SET want='discard',due_at=0 WHERE session=? AND state='closing' AND want='close'`, session)
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
		for _, a := range archives {
			if a.Session != session {
				return sql.ErrNoRows
			}
			_, err := tx.ExecContext(ctx, "INSERT INTO archives(session,repo,ref,commit_sha,created_at,expires_at) VALUES(?,?,?,?,?,?) ON CONFLICT(session,repo) DO UPDATE SET ref=excluded.ref,commit_sha=excluded.commit_sha,created_at=excluded.created_at,expires_at=excluded.expires_at", a.Session, a.Repo, a.Ref, a.Commit, a.CreatedAt.UnixMilli(), a.ExpiresAt.UnixMilli())
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// SetArchiveRetry records a retry only if the observed close intent still matches.
func (s *Store) SetArchiveRetry(ctx context.Context, id contract.BoxID, want BoxWant, due time.Time, reason string, event contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var previous string
		err := tx.QueryRowContext(ctx, "SELECT reason FROM boxes WHERE id=? AND state='closing' AND want=?", id, want).Scan(&previous)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE boxes SET due_at=?,reason=?,updated_at=? WHERE id=? AND state='closing' AND want=?", due.UnixMilli(), reason, event.Time.UnixMilli(), id, want)
		if err != nil {
			return err
		}
		if previous != reason {
			_, err = s.AppendEvent(tx, event)
		}
		return err
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
