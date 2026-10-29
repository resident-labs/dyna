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

// DefaultInterval is the spacing a new parent check-in starts with. The hook
// that runs the pass owns the real interval and sets Interval.
const DefaultInterval = 15 * time.Minute

// dueSlack is the most a trigger can deliver a check-in early. Pass caps it at
// half the interval to preserve a delay between deliveries.
const dueSlack = time.Minute

// Target identifies an open parent with active direct children.
type Target struct{ ID, Room string }

// Scheduler keeps one durable due time per eligible parent. Delivery must
// return false while a parent is busy; the pending row then waits for an event.
type Scheduler struct {
	Root     string
	Now      func() time.Time
	Interval func() (time.Duration, error)
	// IntervalFor adjusts the global interval for one room. Zero turns that
	// room's check-ins off. Nil means every room uses the global interval.
	IntervalFor func(room string, global time.Duration) time.Duration
	Targets     func() ([]Target, error)
	Deliver     func(context.Context, Target, string) (bool, error)
	// Expedite names parents whose check-in is due now, whatever the schedule
	// says. A pass sets their row due at once; one row per parent keeps it to one post.
	Expedite map[string]bool
}

func Migrate(root string) error {
	db, err := database.Open(root)
	if err != nil {
		return err
	}
	return database.Migrate(db, "parent_checkins.schema", 2, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		_, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS parent_checkins (
   session TEXT PRIMARY KEY, room TEXT NOT NULL, due INTEGER NOT NULL, pending INTEGER NOT NULL DEFAULT 0, interval_ns INTEGER NOT NULL, delivered INTEGER NOT NULL DEFAULT 0
  )`)
		if err != nil || old != 1 {
			return err
		}
		_, err = conn.ExecContext(ctx, "ALTER TABLE parent_checkins ADD COLUMN delivered INTEGER NOT NULL DEFAULT 0")
		return err
	})
}

type entry struct {
	Target
	due       int64
	pending   bool
	interval  int64
	delivered int64
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
	intervals := map[string]time.Duration{}
	adjust := s.IntervalFor
	if adjust == nil {
		adjust = func(_ string, global time.Duration) time.Duration { return global }
	}
	// effective is the interval one room checks in with, read once per pass.
	effective := func(roomID string) time.Duration {
		every, ok := intervals[roomID]
		if !ok {
			every = adjust(roomID, interval)
			intervals[roomID] = every
		}
		return every
	}
	active := map[string]Target{}
	for _, target := range targets {
		if effective(target.Room) > 0 {
			active[target.ID] = target
		}
	}
	var entries []entry
	err = state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, "SELECT session,room,due,pending,interval_ns,delivered FROM parent_checkins")
		if err != nil {
			return err
		}
		for rows.Next() {
			var e entry
			if err := rows.Scan(&e.ID, &e.Room, &e.due, &e.pending, &e.interval, &e.delivered); err != nil {
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
			every := effective(e.Room)
			if e.interval != int64(every) {
				e.due, e.pending, e.interval = now.Add(every).UnixNano(), false, int64(every)
				if _, err := conn.ExecContext(ctx, "UPDATE parent_checkins SET due=?,pending=0,interval_ns=? WHERE session=?", e.due, e.interval, e.ID); err != nil {
					return err
				}
			}
			// A turn end expedites at most one check-in per interval: a delivery
			// within the last interval, or a pending row, blocks it.
			expedite := !e.pending && s.Expedite[e.ID] && e.delivered+int64(every) <= now.UnixNano()
			if expedite {
				e.due = now.UnixNano()
			}
			if (expedite || e.due <= now.Add(min(dueSlack, every/2)).UnixNano()) && !e.pending {
				if _, err := conn.ExecContext(ctx, "UPDATE parent_checkins SET due=?,pending=1 WHERE session=?", e.due, e.ID); err != nil {
					return err
				}
				e.pending = true
			}
			kept = append(kept, e)
		}
		entries = kept
		for _, target := range active {
			every := effective(target.Room)
			e := entry{Target: target, due: now.Add(every).UnixNano(), interval: int64(every)}
			if s.Expedite[target.ID] {
				e.due, e.pending = now.UnixNano(), true
			}
			if _, err := conn.ExecContext(ctx, "INSERT INTO parent_checkins(session,room,due,pending,interval_ns) VALUES(?,?,?,?,?)", e.ID, e.Room, e.due, e.pending, e.interval); err != nil {
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
		due := now.Add(time.Duration(e.interval))
		err = state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
			_, err := conn.ExecContext(ctx, "UPDATE parent_checkins SET due=?,pending=0,delivered=? WHERE session=? AND due=?", due.UnixNano(), now.UnixNano(), e.ID, e.due)
			return err
		})
		if err != nil {
			return posted, now.Add(time.Minute), err
		}
		add(due)
	}
	return posted, next, nil
}
