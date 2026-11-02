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

// ExpireReplayKeys removes keys older than the retry window.
func (s *Store) ExpireReplayKeys(ctx context.Context, before time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM client_replays WHERE created_at<?", before.UnixMilli())
		return err
	})
}

// ExpiredArchives bounds each maintenance pass and preserves the oldest first.
func (s *Store) ExpiredArchives(ctx context.Context, now time.Time) ([]Archive, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT session,repo,ref,commit_sha,expires_at FROM archives WHERE expires_at<=? AND retry_at<=? ORDER BY retry_at,expires_at,session,repo LIMIT 50", now.UnixMilli(), now.UnixMilli())
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

// RetryArchive moves a failed report behind reports not tried yet.
func (s *Store) RetryArchive(ctx context.Context, a Archive, at time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE archives SET retry_at=? WHERE session=? AND repo=? AND ref=? AND commit_sha=? AND expires_at=?", at.UnixMilli(), a.Session, a.Repo, a.Ref, a.Commit, a.ExpiresAt.UnixMilli())
		return err
	})
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
			_, err := tx.ExecContext(ctx, "INSERT INTO archives(session,repo,ref,commit_sha,created_at,expires_at) VALUES(?,?,?,?,?,?) ON CONFLICT(session,repo) DO UPDATE SET ref=excluded.ref,commit_sha=excluded.commit_sha,created_at=excluded.created_at,expires_at=excluded.expires_at,retry_at=0", a.Session, a.Repo, a.Ref, a.Commit, a.CreatedAt.UnixMilli(), a.ExpiresAt.UnixMilli())
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
			_, err := tx.ExecContext(ctx, "INSERT INTO archives(session,repo,ref,commit_sha,created_at,expires_at) VALUES(?,?,?,?,?,?) ON CONFLICT(session,repo) DO UPDATE SET ref=excluded.ref,commit_sha=excluded.commit_sha,created_at=excluded.created_at,expires_at=excluded.expires_at,retry_at=0", a.Session, a.Repo, a.Ref, a.Commit, a.CreatedAt.UnixMilli(), a.ExpiresAt.UnixMilli())
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// OpenAncestor finds the nearest ancestor that is not closing, and whether that is the parent. Root never closes this way.
func (s *Store) OpenAncestor(ctx context.Context, id contract.SessionID) (ancestor contract.SessionID, direct bool, err error) {
	direct = true
	for {
		var state string
		if err := s.Read().QueryRowContext(ctx, "SELECT p.id,p.state FROM sessions c JOIN sessions p ON p.id=c.parent WHERE c.id=?", id).Scan(&id, &state); err != nil {
			return "", false, err
		}
		if state != "closing" {
			return id, direct, nil
		}
		direct = false
	}
}

// HoldArchive ends a close's retries in one write. The reconciler then stops the box. The notice wakes to, and moved, when
// given, records the session moving there. A close that changed meanwhile keeps going, and nothing is sent.
func (s *Store) HoldArchive(ctx context.Context, session, to contract.SessionID, notice contract.Event, moved *contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE boxes SET want='hold',due_at=0 WHERE session=? AND state='closing' AND want='close'`, session)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil || n != 1 {
			return err
		}
		if moved != nil {
			if _, err := tx.ExecContext(ctx, "UPDATE sessions SET parent=? WHERE id=?", to, session); err != nil {
				return err
			}
			if _, err := s.AppendEvent(tx, *moved); err != nil {
				return err
			}
		}
		seq, err := s.AppendEvent(tx, notice)
		if err != nil {
			return err
		}
		return s.InsertDelivery(ctx, tx, seq, notice.Body, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: to}, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true})
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

// SetClosingReason records and counts an archive failure without deleting the box. An empty reason clears the last one for a retry.
func (s *Store) SetClosingReason(ctx context.Context, id contract.BoxID, reason string, event contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE boxes SET reason=?,updated_at=?,failures=failures+(?!='') WHERE id=? AND state=?", reason, event.Time.UnixMilli(), reason, id, contract.BoxClosing)
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
