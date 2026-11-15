package store

import (
	"context"
	"database/sql"
	"errors"
	"github.com/resident-labs/dyna/contract"
	"time"
)

func (s *Store) OwnerTimezone(ctx context.Context) (string, error) {
	var zone string
	err := s.Read().QueryRowContext(ctx, "SELECT value FROM meta WHERE key='owner_timezone'").Scan(&zone)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return zone, err
}
func (s *Store) SetOwnerTimezone(ctx context.Context, zone string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES('owner_timezone',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", zone)
		return err
	})
}

// PreviousOwnerInput follows deliveries, not conversation surfaces. Sequence order also makes batched and replayed frames stable.
func (s *Store) PreviousOwnerInput(ctx context.Context, viewer contract.SessionID, seq int64) (time.Time, error) {
	var at int64
	err := s.Read().QueryRowContext(ctx, `SELECT e.at FROM events e JOIN deliveries d ON d.seq=e.seq
 WHERE d.to_kind='session' AND d.to_id=? AND e.seq<? AND
 (e.from_kind='owner' OR (e.type='discord.message' AND json_extract(e.body,'$.owner')=1))
 ORDER BY e.seq DESC LIMIT 1`, viewer, seq).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	return time.UnixMilli(at), err
}

// TurnStartedAt is the original clock for a turn, including reconnects and steering dispatches.
func (s *Store) TurnStartedAt(ctx context.Context, turn contract.TurnID) (time.Time, error) {
	var at int64
	err := s.Read().QueryRowContext(ctx, "SELECT started_at FROM turns WHERE id=?", turn).Scan(&at)
	return time.UnixMilli(at).UTC(), err
}
