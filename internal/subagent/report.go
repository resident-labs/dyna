package subagent

import (
	"context"
	"fmt"
	"path/filepath"

	"dyna-agent/internal/room"
	"dyna-agent/internal/store"
)

// ReportPlan is the exact post prepared for one terminal turn. The message
// ID and recipient session IDs stay fixed if completion is interrupted.
type ReportPlan struct {
	RoomID string         `json:"roomId"`
	Post   room.PostInput `json:"post"`
}

// LockReport serializes preparation and replay of session reports without
// holding the session mutation lock across artifact or room writes.
func (s *Store) LockReport(ctx context.Context, id string) (func(), error) {
	dir, err := s.Dir(id)
	if err != nil {
		return nil, err
	}
	return store.LockContext(ctx, filepath.Join(dir, "report.lock"))
}

// ClaimReport leaves a terminal report pending until FinishReport confirms
// its durable post or silent result. A pending report can be resumed.
// Callers hold LockReport while preparing and delivering it.
func (s *Store) ClaimReport(ctx context.Context, id string, n int) (Turn, bool, error) {
	var out Turn
	claimed := false
	err := s.withLockContext(ctx, id, func() error {
		turn, err := s.readTurnForUpdate(id, n)
		if err != nil {
			return err
		}
		out = turn
		if !turn.Done() || turn.Reported {
			return nil
		}
		claimed = true
		if turn.ReportPending {
			return nil
		}
		turn.ReportPending = true
		return s.writeTurnContext(ctx, id, turn)
	})
	return out, claimed, err
}

// SaveReportPlan commits the immutable post before its room append. A
// retry keeps the first plan even if metadata or parent names have changed.
func (s *Store) SaveReportPlan(ctx context.Context, id string, n int, plan ReportPlan) (ReportPlan, error) {
	var saved ReportPlan
	err := s.withLockContext(ctx, id, func() error {
		turn, err := s.readTurnForUpdate(id, n)
		if err != nil {
			return err
		}
		if turn.Reported {
			return nil
		}
		if turn.ReportPlan != nil {
			saved = *turn.ReportPlan
			return nil
		}
		turn.ReportPending, turn.ReportPlan = true, &plan
		if err := s.writeTurnContext(ctx, id, turn); err != nil {
			return err
		}
		saved = plan
		return nil
	})
	return saved, err
}

// FinishReport acknowledges a stored post and its durable wake intent, or
// a deliberate silent result. It never precedes the room append.
func (s *Store) FinishReport(ctx context.Context, id string, n int) error {
	_, err := s.updateTurnContext(ctx, id, n, func(turn *Turn) {
		turn.Reported, turn.ReportPending = true, false
		turn.ReportPlan = nil
	})
	return err
}

// PendingReport identifies unfinished completion work in a terminal turn row.
type PendingReport struct {
	SessionID string
	Turn      int
}

// PendingReports reads only rows in the pending-report index. sessionID
// restricts recovery to an exited runner; empty reads startup work.
func (s *Store) PendingReports(ctx context.Context, sessionID string) ([]PendingReport, error) {
	db, err := s.turnsDB()
	if err != nil {
		return nil, err
	}
	query := `SELECT session, n FROM session_turns
WHERE json_extract(CAST(turn AS TEXT), '$.reportPending') = 1`
	var args []any
	if sessionID != "" {
		query += " AND session = ?"
		args = append(args, sessionID)
	}
	query += " ORDER BY session, n"
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pending []PendingReport
	for rows.Next() {
		var report PendingReport
		if err := rows.Scan(&report.SessionID, &report.Turn); err != nil {
			return nil, err
		}
		pending = append(pending, report)
	}
	return pending, rows.Err()
}

// PendReport records recoverable work before its worker can be scheduled.
// A final historical report is left alone.
func (s *Store) PendReport(ctx context.Context, id string, n int) error {
	if n < 1 {
		return fmt.Errorf("completion requires a positive turn number")
	}
	turn, _, err := s.ClaimReport(ctx, id, n)
	if err != nil {
		return err
	}
	if !turn.Done() {
		return fmt.Errorf("session %s turn %d is still %s", id, n, turn.Status)
	}
	return nil
}
