package shells

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"dyna-agent/internal/activity"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// activityReader observes raw reads before line buffering and output truncation.
type activityReader struct {
	io.Reader
	now      func() time.Time
	deadline func(time.Time) error
	publish  func(activity.Signal)
	signal   activity.Signal
}

func (r *activityReader) start() {
	r.signal.Copier = activity.CopierUnknown
	r.signal.CPU = activity.CPUUnsupported
	r.publish(r.signal)
}

func (r *activityReader) Read(p []byte) (int, error) {
	for {
		// Only the copier can prove it is still observing the pipe.
		proving := r.deadline != nil && r.deadline(r.now().Add(activity.PublishInterval)) == nil
		if !proving {
			r.signal.Copier = activity.CopierUnknown
			r.signal.ObservedSince = time.Time{}
			r.signal.LeaseUntil = time.Time{}
			r.publish(r.signal)
		}
		n, err := r.Reader.Read(p)
		if proving && (n > 0 || errors.Is(err, os.ErrDeadlineExceeded)) {
			r.signal = r.signal.Prove(r.now())
			if n > 0 {
				r.signal.LastOutput = r.now()
			}
			r.publish(r.signal)
		} else if n > 0 {
			r.signal.LastOutput = r.now()
			r.publish(r.signal)
		}
		if proving && errors.Is(err, os.ErrDeadlineExceeded) && n == 0 {
			continue
		}
		return n, err
	}
}

func (r *activityReader) end(err error) {
	r.signal.Copier = activity.CopierEnded
	if err != io.EOF {
		r.signal.Copier = activity.CopierUnknown
	}
	r.publish(r.signal)
}

// activityPublisher coalesces observations without ever blocking the copier.
// The publisher flushes at most once per tick, except for the final flush.
// Failed writes cannot renew the stored lease.
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
	var storedLease time.Time
	dirty := false
	for {
		select {
		case s, ok := <-p.updates:
			if !ok {
				if dirty {
					_ = write(pending)
				}
				return
			}
			pending, dirty = s, true
		case <-ticks:
			if dirty {
				proof := pending.LeaseUntil.Add(-activity.Lease)
				if pending.Copier == activity.CopierRunning && (storedLease.IsZero() || !proof.Before(storedLease)) {
					pending.ObservedSince = proof
				}
				if write(pending) == nil {
					storedLease = pending.LeaseUntil
				}
				dirty = false
			}
		}
	}
}

func (s *Store) publishActivity(db *sql.DB, id string, signal activity.Signal) error {
	ctx, cancel := context.WithTimeout(context.Background(), writeWait)
	defer cancel()
	var last, since, lease int64
	if !signal.LeaseUntil.IsZero() {
		lease = signal.LeaseUntil.UnixNano()
	}
	if !signal.ObservedSince.IsZero() {
		since = signal.ObservedSince.UnixNano()
	}
	if !signal.LastOutput.IsZero() {
		last = signal.LastOutput.UnixNano()
	}
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `UPDATE shell_jobs SET activity_output=?, activity_lease=?, activity_copier=?, activity_cpu=?, activity_since=? WHERE id=?`,
			last, lease, signal.Copier, signal.CPU, since, id)
		return err
	})
}

// Activity reads the last published shell observation. A missing or expired
// lease is unknown; only copier proof can renew its lease.
func (s *Store) Activity(id string, now time.Time) (activity.Signal, error) {
	if !ValidID(id) {
		return activity.Signal{}, fmt.Errorf("invalid shell id %q", id)
	}
	db, err := s.db()
	if err != nil {
		return activity.Signal{}, err
	}
	var last, lease, since int64
	var signal activity.Signal
	err = db.QueryRow(`SELECT activity_output, activity_lease, activity_copier, activity_cpu, activity_since FROM shell_jobs WHERE id=?`, id).Scan(&last, &lease, &signal.Copier, &signal.CPU, &since)
	if err != nil {
		return activity.Signal{}, err
	}
	if since != 0 {
		signal.ObservedSince = time.Unix(0, since)
	}
	if last != 0 {
		signal.LastOutput = time.Unix(0, last)
	}
	if lease != 0 {
		signal.LeaseUntil = time.Unix(0, lease)
	}
	return signal.At(now), nil
}
