package store

import (
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/contract"
	"time"
)

type HostexecStop string

const (
	HostexecRevoked HostexecStop = "revoked"
	HostexecLost    HostexecStop = "lost"
	HostexecTimeout HostexecStop = "timeout"
)

type HostexecRun struct {
	Op      contract.OpID
	Grant   contract.GrantID
	Host    contract.HostName
	Scope   string
	Timeout contract.DurationMS
	Stopped HostexecStop
}

// StartHostexec serializes the last grant check with revocation.
func (s *Store) StartHostexec(ctx context.Context, run HostexecRun, who contract.Principal, room contract.RoomID, now time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		r, err := tx.Exec(`INSERT INTO hostexec_runs(op,grant_id,host,scope,started_at,timeout_ms) SELECT ?,id,?,?,?,? FROM grants WHERE id=? AND room=? AND to_kind=? AND to_id=? AND revoked_at IS NULL AND expired_at IS NULL AND (expires_at IS NULL OR expires_at>?)`, run.Op, run.Host, run.Scope, now.UnixMilli(), run.Timeout, run.Grant, room, who.Kind, who.Session, now.UnixMilli())
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
}
func (s *Store) FinishHostexec(ctx context.Context, id contract.OpID, now time.Time, bytes int, stopped HostexecStop) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE hostexec_runs SET ended_at=?,output_bytes=?,stopped=CASE WHEN stopped='revoked' THEN stopped ELSE ? END WHERE op=?", now.UnixMilli(), bytes, stopped, id)
		return err
	})
}
func (s *Store) HostexecStops(ctx context.Context, now time.Time) ([]HostexecRun, error) {
	rows, err := s.read.QueryContext(ctx, "SELECT op,grant_id,host,scope,stopped FROM hostexec_runs WHERE (stopped='revoked' AND ended_at IS NULL) OR (stopped='lost' AND started_at+timeout_ms+10000>?)", now.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HostexecRun
	for rows.Next() {
		var r HostexecRun
		if err = rows.Scan(&r.Op, &r.Grant, &r.Host, &r.Scope, &r.Stopped); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecoverHostexec marks interrupted runs for the lost-unit stop sweep.
func (s *Store) RecoverHostexec(ctx context.Context, now time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE hostexec_runs SET stopped='lost',ended_at=? WHERE ended_at IS NULL AND stopped=''", now.UnixMilli())
		return err
	})
}
