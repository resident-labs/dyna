package store

import (
	"context"
	"database/sql"
	"errors"
	"github.com/resident-labs/dyna/contract"
	"time"
)

var ErrGrantInactive = errors.New("grant no longer active")

type HostexecStop string

const (
	HostexecClosed  HostexecStop = "closed"
	HostexecLost    HostexecStop = "lost"
	HostexecTimeout HostexecStop = "timeout"
	HostexecStopped HostexecStop = "stopped" // its session stopped it, as with job stop
)

type HostexecRun struct {
	Op      contract.OpID
	Grant   contract.GrantID
	Host    contract.HostName
	Scope   string
	Timeout contract.DurationMS
	Stopped HostexecStop
}

// StartHostexec serializes the last grant check with session close.
func (s *Store) StartHostexec(ctx context.Context, run HostexecRun, who contract.Principal, room contract.RoomID, now time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		r, err := tx.Exec(`INSERT INTO hostexec_runs(op,grant_id,host,scope,started_at,timeout_ms) SELECT ?,id,?,?,?,? FROM grants WHERE id=? AND room=? AND to_kind=? AND to_id=? AND revoked_at IS NULL AND expired_at IS NULL AND EXISTS (SELECT 1 FROM sessions WHERE id=grants.to_id AND state!='closing' AND closed_at IS NULL)`, run.Op, run.Host, run.Scope, now.UnixMilli(), run.Timeout, run.Grant, room, who.Kind, who.Session)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrGrantInactive
		}
		return nil
	})
}

// FinishHostexec records a run's end and returns how it stopped, which a stop during the run decides.
func (s *Store) FinishHostexec(ctx context.Context, id contract.OpID, now time.Time, bytes int, stopped HostexecStop) (HostexecStop, error) {
	var final HostexecStop
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if stopped == HostexecLost {
			_, err := tx.Exec("UPDATE hostexec_runs SET stopped='lost',lost_at=?,output_bytes=? WHERE op=?", now.UnixMilli(), bytes, id)
			final = stopped
			return err
		}
		if _, err := tx.Exec("UPDATE hostexec_runs SET ended_at=?,output_bytes=?,stopped=CASE WHEN stopped IN ('closed','revoked','stopped') THEN stopped ELSE ? END WHERE op=?", now.UnixMilli(), bytes, stopped, id); err != nil {
			return err
		}
		return tx.QueryRow("SELECT stopped FROM hostexec_runs WHERE op=?", id).Scan(&final)
	})
	return final, err
}

// StopHostexecRun marks a running run stopped and returns it. ok is false for a run that has ended or is being stopped
// already, and the returned run is empty for one that has not started.
func (s *Store) StopHostexecRun(ctx context.Context, id contract.OpID) (HostexecRun, bool, error) {
	var run HostexecRun
	var ok bool
	err := s.Write(ctx, func(tx *sql.Tx) error {
		run, ok = HostexecRun{}, false
		var ended sql.NullInt64
		err := tx.QueryRow("SELECT op,grant_id,host,scope,timeout_ms,stopped,ended_at FROM hostexec_runs WHERE op=?", id).Scan(&run.Op, &run.Grant, &run.Host, &run.Scope, &run.Timeout, &run.Stopped, &ended)
		if errors.Is(err, sql.ErrNoRows) {
			run = HostexecRun{}
			return nil
		}
		if err != nil || ended.Valid || run.Stopped != "" {
			return err
		}
		if _, err := tx.Exec("UPDATE hostexec_runs SET stopped='stopped' WHERE op=?", id); err != nil {
			return err
		}
		run.Stopped, ok = HostexecStopped, true
		return nil
	})
	return run, ok, err
}
func (s *Store) HostexecStops(ctx context.Context, now time.Time) ([]HostexecRun, error) {
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE hostexec_runs SET ended_at=? WHERE ended_at IS NULL AND lost_at IS NOT NULL AND lost_at+30000+timeout_ms+10000<=?", now.UnixMilli(), now.UnixMilli())
		return err
	}); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, "SELECT op,grant_id,host,scope,stopped FROM hostexec_runs WHERE ended_at IS NULL AND stopped!=''")
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
		_, err := tx.Exec("UPDATE hostexec_runs SET stopped='lost',lost_at=? WHERE ended_at IS NULL", now.UnixMilli())
		return err
	})
}

// CompleteHostexecStop waits 30 seconds for a remote launch in flight at a master drop.
// Closed-session runs retain their live runner, which records completion itself.
func (s *Store) CompleteHostexecStop(ctx context.Context, id contract.OpID, now time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE hostexec_runs SET ended_at=? WHERE op=? AND ended_at IS NULL AND lost_at IS NOT NULL AND lost_at+30000<=?", now.UnixMilli(), id, now.UnixMilli())
		return err
	})
}
