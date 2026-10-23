// Package parentcheckin schedules parent reviews without interrupting turns.
package parentcheckin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// Target identifies an open parent with active direct children.
type Target struct{ ID, Room string }

// Scheduler keeps one durable due time per eligible parent. Delivery must
// return false while a parent is busy; the pending row then waits for an event.
type Scheduler struct {
	Root     string
	Now      func() time.Time
	Interval func() (time.Duration, error)
	Targets  func() ([]Target, error)
	Deliver  func(context.Context, Target, string) (bool, error)
}

func Migrate(root string) error {
	db, err := database.Open(root)
	if err != nil {
		return err
	}
	return database.Migrate(db, "parent_checkins.schema", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), `CREATE TABLE IF NOT EXISTS parent_checkins (
   session TEXT PRIMARY KEY, room TEXT NOT NULL, due INTEGER NOT NULL, pending INTEGER NOT NULL DEFAULT 0, interval_ns INTEGER NOT NULL
  )`)
		return err
	})
}

type entry struct {
	Target
	due      int64
	pending  bool
	interval int64
}

// Tick reconciles eligibility, delivers due work, and returns the next deadline.
// Pending rows have no timer; member and turn-end events retry them.
func (s *Scheduler) Tick(ctx context.Context) (time.Time, error) {
	_, next, err := s.Pass(ctx)
	return next, err
}

// Pass runs one check-in pass: it delivers every due check-in and reports how
// many it posted, with the next deadline. A job that runs a pass on its own
// uses the count; Tick uses the deadline to time the engine's worker.
func (s *Scheduler) Pass(ctx context.Context) (int, time.Time, error) {
	posted := 0
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	interval, err := s.Interval()
	if err != nil {
		return posted, time.Time{}, err
	}
	targets, err := s.Targets()
	if err != nil {
		return posted, time.Time{}, err
	}
	db, err := database.Open(s.Root)
	if err != nil {
		return posted, time.Time{}, err
	}
	active := map[string]Target{}
	if interval > 0 {
		for _, target := range targets {
			active[target.ID] = target
		}
	}
	var entries []entry
	err = state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, "SELECT session,room,due,pending,interval_ns FROM parent_checkins")
		if err != nil {
			return err
		}
		for rows.Next() {
			var e entry
			if err := rows.Scan(&e.ID, &e.Room, &e.due, &e.pending, &e.interval); err != nil {
				rows.Close()
				return err
			}
			entries = append(entries, e)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		kept := entries[:0]
		for _, e := range entries {
			if _, ok := active[e.ID]; !ok {
				if _, err := conn.ExecContext(ctx, "DELETE FROM parent_checkins WHERE session=?", e.ID); err != nil {
					return err
				}
				continue
			}
			delete(active, e.ID)
			if e.interval != int64(interval) {
				e.due, e.pending, e.interval = now.Add(interval).UnixNano(), false, int64(interval)
				if _, err := conn.ExecContext(ctx, "UPDATE parent_checkins SET due=?,pending=0,interval_ns=? WHERE session=?", e.due, e.interval, e.ID); err != nil {
					return err
				}
			}
			if e.due <= now.UnixNano() && !e.pending {
				if _, err := conn.ExecContext(ctx, "UPDATE parent_checkins SET pending=1 WHERE session=?", e.ID); err != nil {
					return err
				}
				e.pending = true
			}
			kept = append(kept, e)
		}
		entries = kept
		for _, target := range active {
			e := entry{Target: target, due: now.Add(interval).UnixNano()}
			if _, err := conn.ExecContext(ctx, "INSERT INTO parent_checkins(session,room,due,interval_ns) VALUES(?,?,?,?)", e.ID, e.Room, e.due, int64(interval)); err != nil {
				return err
			}
			entries = append(entries, e)
		}
		return nil
	})
	if err != nil {
		return posted, time.Time{}, err
	}
	var next time.Time
	add := func(at time.Time) {
		if next.IsZero() || at.Before(next) {
			next = at
		}
	}
	for _, e := range entries {
		if !e.pending {
			add(time.Unix(0, e.due))
			continue
		}
		// The stable ID makes a retry after a crash reuse the same room post.
		digest := sha256.Sum256([]byte(fmt.Sprintf("parent-checkin/%s/%d", e.ID, e.due)))
		id := fmt.Sprintf("msg_%x", digest[:8])
		delivered, err := s.Deliver(ctx, e.Target, id)
		if err != nil {
			return posted, now.Add(time.Minute), err
		}
		if !delivered {
			continue
		}
		posted++
		due := now.Add(interval)
		err = state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
			_, err := conn.ExecContext(ctx, "UPDATE parent_checkins SET due=?,pending=0 WHERE session=? AND due=?", due.UnixNano(), e.ID, e.due)
			return err
		})
		if err != nil {
			return posted, now.Add(time.Minute), err
		}
		add(due)
	}
	return posted, next, nil
}
