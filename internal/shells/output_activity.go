package shells

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"time"

	"dyna-agent/internal/activity"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// activityReader observes raw reads before line buffering and output truncation.
type activityReader struct {
	io.Reader
	now     func() time.Time
	publish func(activity.Signal)
	signal  activity.Signal
}

func (r *activityReader) start() {
	r.signal.Copier = activity.CopierRunning
	r.publish(r.signal)
}

func (r *activityReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if n > 0 {
		r.signal.LastOutput = r.now()
		r.publish(r.signal)
	}
	return n, err
}

func (r *activityReader) end(err error) {
	r.signal.Copier = activity.CopierEnded
	if err != io.EOF {
		r.signal.Copier = activity.CopierUnknown
	}
	r.publish(r.signal)
}

// activityPublisher coalesces observations without ever blocking the copier.
// The publisher flushes at most once per tick. Failed writes cannot renew the
// stored lease. An ended observation is best effort when the supervisor exits.
type activityPublisher struct {
	updates chan activity.Signal
}

func (p *activityPublisher) send(s activity.Signal) {
	select {
	case p.updates <- s:
	default:
		select {
		case <-p.updates:
		default:
		}
		select {
		case p.updates <- s:
		default:
		}
	}
}

func (p *activityPublisher) run(ticks <-chan time.Time, write func(activity.Signal) error) {
	var pending activity.Signal
	dirty := false
	for {
		select {
		case s, ok := <-p.updates:
			if !ok {
				if dirty {
					now := <-ticks
					pending.LeaseUntil = now.Add(activity.Lease)
					_ = write(pending)
				}
				return
			}
			pending, dirty = s, true
		case now := <-ticks:
			if dirty {
				pending.LeaseUntil = now.Add(activity.Lease)
				_ = write(pending)
				dirty = false
			}
		}
	}
}

func (s *Store) publishActivity(db *sql.DB, id string, signal activity.Signal) error {
	ctx, cancel := context.WithTimeout(context.Background(), writeWait)
	defer cancel()
	var last int64
	if !signal.LastOutput.IsZero() {
		last = signal.LastOutput.UnixNano()
	}
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `UPDATE shell_jobs SET activity_output=?, activity_lease=?, activity_copier=? WHERE id=?`,
			last, signal.LeaseUntil.UnixNano(), signal.Copier, id)
		return err
	})
}

// Activity reads the last published shell observation. A missing or expired
// lease is unknown; a quiet copier does not renew its lease.
func (s *Store) Activity(id string, now time.Time) (activity.Signal, error) {
	if !ValidID(id) {
		return activity.Signal{}, fmt.Errorf("invalid shell id %q", id)
	}
	db, err := s.db()
	if err != nil {
		return activity.Signal{}, err
	}
	var last, lease int64
	var signal activity.Signal
	err = db.QueryRow(`SELECT activity_output, activity_lease, activity_copier FROM shell_jobs WHERE id=?`, id).Scan(&last, &lease, &signal.Copier)
	if err != nil {
		return activity.Signal{}, err
	}
	if last != 0 {
		signal.LastOutput = time.Unix(0, last)
	}
	if lease != 0 {
		signal.LeaseUntil = time.Unix(0, lease)
	}
	return signal.At(now), nil
}
