package hookinstall

import (
	"context"
	"database/sql"
	"time"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/localci"
	prstate "dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
	statewrite "dyna-agent/internal/state"
	"dyna-agent/internal/store"
	"dyna-agent/internal/updatedrain"
)

// updateBusy reads all rooms immediately before an automatic restart. A new
// run can still start after this read; this is not an admission fence.
func updateBusy(ctx context.Context) (bool, error) {
	st, err := prstate.Read(prstate.Root(store.DataDir()))
	if err != nil {
		return false, err
	}
	for _, trial := range st.MergeTrials {
		if prstate.TrialActive(trial.Phase) {
			return true, nil
		}
	}
	runs, err := runstore.Running()
	if err != nil {
		return false, err
	}
	if len(runs) > 0 {
		return true, nil
	}
	return (localci.Store{DataDir: store.DataDir()}).HasActiveAttachments(ctx)
}

// updateGuard keeps a machine-wide cooldown and one notice per busy period.
// The installer is serialized by its existing lock.
func (s Service) updateGuard(ctx context.Context, now time.Time, source string) (bool, error) {
	busy, err := updateBusy(ctx)
	if err != nil {
		return false, err
	}
	db, err := s.Jobs.db()
	if err != nil {
		return false, err
	}
	err = statewrite.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS update_restart_guard (
id INTEGER PRIMARY KEY CHECK(id=1), last_restart INTEGER NOT NULL, busy_since INTEGER NOT NULL, noticed INTEGER NOT NULL)`); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, "INSERT OR IGNORE INTO update_restart_guard VALUES(1,0,0,0)")
		return err
	})
	if err != nil {
		return false, err
	}
	var last, since int64
	var noticed bool
	if err = db.QueryRowContext(ctx, "SELECT last_restart,busy_since,noticed FROM update_restart_guard WHERE id=1").Scan(&last, &since, &noticed); err != nil {
		return false, err
	}
	if busy {
		if since == 0 {
			since = now.Unix()
			if err = statewrite.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
				_, err := conn.ExecContext(ctx, "UPDATE update_restart_guard SET busy_since=? WHERE id=1", since)
				return err
			}); err != nil {
				return false, err
			}
		}
		if !noticed && now.Sub(time.Unix(since, 0)) >= 3*time.Hour {
			notify := s.UpdateNotice
			if notify == nil {
				notify = noticeUpdateWait
			}
			if err = notify(source); err != nil {
				return false, err
			}
			if err = statewrite.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
				_, err := conn.ExecContext(ctx, "UPDATE update_restart_guard SET noticed=1 WHERE id=1")
				return err
			}); err != nil {
				return false, err
			}
		}
		return false, nil
	}
	if since != 0 || noticed {
		if err = statewrite.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
			_, err := conn.ExecContext(ctx, "UPDATE update_restart_guard SET busy_since=0,noticed=0 WHERE id=1")
			return err
		}); err != nil {
			return false, err
		}
	}
	if last != 0 && now.Sub(time.Unix(last, 0)) < time.Hour {
		return false, nil
	}
	return true, nil
}

func noticeUpdateWait(source string) error {
	rooms := room.DefaultStore()
	rec, err := rooms.EnsureRepoRoom(source, "")
	if err != nil {
		return err
	}
	_, err = rooms.PostEventInput(room.PostEventInput{
		RoomID: rec.ID, Producer: room.ProducerEngine, NeedsRoot: true, To: []string{rec.Name},
		Event: room.AlertEvent{Source: room.AlertEngine, Summary: "An update has waited 3 hours for work to finish.", Detail: "Active workflows, merge trials or local CI keep the engine update waiting. Choose a quiet time or run Updates manually."},
	})
	return err
}

func (s Service) recordUpdateRestart(ctx context.Context, now time.Time) error {
	db, err := s.Jobs.db()
	if err != nil {
		return err
	}
	return statewrite.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS update_restart_guard (
id INTEGER PRIMARY KEY CHECK(id=1), last_restart INTEGER NOT NULL, busy_since INTEGER NOT NULL, noticed INTEGER NOT NULL)`); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, "INSERT INTO update_restart_guard VALUES(1,?,0,0) ON CONFLICT(id) DO UPDATE SET last_restart=excluded.last_restart,busy_since=0,noticed=0", now.Unix())
		return err
	})
}

// updateMayBuild allows a long-waiting update to prepare its binary while CI
// continues. The restart cooldown still applies.
func (s Service) updateMayBuild(ctx context.Context, now time.Time) (bool, error) {
	db, err := s.Jobs.db()
	if err != nil {
		return false, err
	}
	var last, since int64
	if err := db.QueryRowContext(ctx, `SELECT last_restart,busy_since FROM update_restart_guard WHERE id=1`).Scan(&last, &since); err != nil {
		return false, err
	}
	return since != 0 && now.Sub(time.Unix(since, 0)) >= 30*time.Minute && (last == 0 || now.Sub(time.Unix(last, 0)) >= time.Hour), nil
}

// waitForDrain gives admitted work a bounded time to finish. A cancelled
// installer releases its fence without restarting.
func waitForDrain(ctx context.Context, db *sql.DB, budget time.Duration) (bool, error) {
	timer := time.NewTimer(budget)
	defer timer.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		busy, err := updateBusy(ctx)
		if err != nil || !busy {
			return !busy, err
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-timer.C:
			return false, nil
		case <-ticker.C:
			if err := updatedrain.Refresh(ctx, db); err != nil {
				return false, err
			}
		}
	}
}
