package hooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/repoid"
	statewrite "dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

// Enqueue persists an event without running hooks. The queue row's
// autoincrement id preserves append order, including concurrent writers in
// other processes.
func (s Service) Enqueue(e Event) error {

	source := e.Worktree
	if source == "" {
		source = e.Repo
	}
	r, err := repoid.Resolve(context.Background(), source)
	if err != nil {
		return err
	}
	e.Repo = r.Main
	if e.Worktree == "" {
		e.Worktree = r.Root
	}
	// Capture only private-tree inputs here. Main-checkout hooks sample their
	// current Git values when they run, as they did before deferred preparation.
	if !e.Snapshot && needsSnapshot(e) {
		if e.SHA == "" {
			e.SHA, err = repoid.Git(context.Background(), e.Worktree, "rev-parse", "HEAD")
			if err != nil {
				return err
			}
		} else if !validCommitSHA(e.SHA) {
			e.SHA, err = repoid.Git(context.Background(), e.Worktree, "rev-parse", "--verify", "--end-of-options", e.SHA+"^{commit}")
			if err != nil {
				return err
			}
		}
		if e.Branch == "" {
			e.Branch, _ = repoid.Git(context.Background(), e.Worktree, "symbolic-ref", "--short", "-q", "HEAD")
		}
	}
	return s.appendQueued(context.Background(), e)
}

// drainRetry is how soon a drain that could not finish runs again.
const drainRetry = time.Minute

// drainLock is the flock that keeps one drain running at a time, in any
// process. An flock releases on crash, and a drain of any length keeps it.
func (s Service) drainLock() (unlock func(), owned bool, err error) {
	dir := filepath.Join(s.dataDir(), "hooks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, false, err
	}
	return store.TryLock(filepath.Join(dir, "queue-drain.lock"))
}

// Drain runs each queued event through Dispatch, then removes its row, and
// returns the ids of the rows it ran. Hook failures are reported and consumed.
// A drain that cannot finish (another process drains, or a database error
// stops it) returns when to try again, counted from now; otherwise retry is
// zero. Cancellation leaves the event for the next engine. A crash after
// Dispatch but before removal can run the event again.
func (s Service) Drain(ctx context.Context, now time.Time) (ran []string, retry time.Time, err error) {
	retry = now.Add(drainRetry)
	unlock, owned, err := s.drainLock()
	if err != nil || !owned {
		return nil, retry, err
	}
	defer unlock()
	// Events a previous-release emitter still appends as files join the queue
	// here, until no old emitter remains.
	if err := s.importSpoolFiles(ctx); err != nil {
		return nil, retry, err
	}
	pending, err := s.queued(ctx)
	if err != nil {
		return nil, retry, err
	}
	var errs []error
	for _, row := range pending {
		if err := ctx.Err(); err != nil {
			return ran, time.Time{}, errors.Join(append(errs, err)...)
		}
		errs = append(errs, s.dispatchQueued(ctx, row.event))
		if err := ctx.Err(); err != nil {
			return ran, time.Time{}, errors.Join(append(errs, err)...)
		}
		if err := s.removeQueued(ctx, row.id); err != nil {
			return ran, retry, errors.Join(append(errs, err)...)
		}
		ran = append(ran, strconv.FormatInt(row.id, 10))
		if row.event.Snapshot {
			cleanupSnapshot(row.event)
		}
	}
	return ran, time.Time{}, errors.Join(errs...)
}

// queuedRow is one pending row: its id, for removal, and its event.
type queuedRow struct {
	id    int64
	event Event
}

// queued reads the pending rows, oldest first. It is the snapshot one drain
// works through; rows appended later wait for the next drain.
func (s Service) queued(ctx context.Context) ([]queuedRow, error) {
	db, err := s.queueDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, "SELECT id,body FROM hook_queue ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pending := []queuedRow{}
	for rows.Next() {
		var row queuedRow
		var body []byte
		if err := rows.Scan(&row.id, &body); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &row.event); err != nil {
			return nil, fmt.Errorf("hook queue row %d: %w", row.id, err)
		}
		pending = append(pending, row)
	}
	return pending, rows.Err()
}

// QueuedEvents returns the pending events, oldest first.
func (s Service) QueuedEvents(ctx context.Context) ([]Event, error) {
	pending, err := s.queued(ctx)
	if err != nil {
		return nil, err
	}
	events := make([]Event, len(pending))
	for i, row := range pending {
		events[i] = row.event
	}
	return events, nil
}

// removeQueued deletes one row whose event has run.
func (s Service) removeQueued(ctx context.Context, id int64) error {
	db, err := s.queueDB()
	if err != nil {
		return err
	}
	return statewrite.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, "DELETE FROM hook_queue WHERE id=?", id)
		return err
	})
}

// needsSnapshot uses the permanent main checkout that Enqueue stores in Repo.
func needsSnapshot(e Event) bool {
	return e.Event == BeforeLand || e.Worktree != e.Repo
}

// dispatchQueued creates a captured commit's execution tree on the worker.
func (s Service) dispatchQueued(ctx context.Context, e Event) error {
	// TODO: legacy/shim remove later — drains inbox events whose previous-release producer already created their snapshot; remove with item 31.
	if e.Snapshot {
		return s.Dispatch(ctx, e)
	}
	if !needsSnapshot(e) {
		return s.Dispatch(ctx, e)
	}
	prepared, err := snapshot(ctx, e)
	if err == nil {
		defer cleanupSnapshot(prepared)
	} else {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err = fmt.Errorf("prepare queued hook snapshot at %q: %w", e.SHA, err)
	}
	return s.dispatch(ctx, prepared, err)
}

// SpoolVersion changes when rows enter or leave the queue. The store watcher
// uses this cheap query to wake the engine without reading each event.
func (s Service) SpoolVersion() string {
	db, err := s.queueDB()
	if err != nil {
		return ""
	}
	var version string
	if err := db.QueryRow("SELECT count(*)||':'||coalesce(max(id),0) FROM hook_queue").Scan(&version); err != nil {
		return ""
	}
	return version
}
