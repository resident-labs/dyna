package hookinstall

import (
	"context"
	"time"

	"dyna-agent/internal/localci"
	prstate "dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store"
)

// updateBusy reads all rooms immediately before an automatic restart. A new
// run can still start after this read; this is not an admission fence.
func updateBusy(ctx context.Context) (bool, error) {
	st, err := prstate.Read(prstate.Root(store.DataDir()))
	if err != nil {
		return false, err
	}
	for _, trial := range st.MergeTrials {
		switch trial.Phase {
		case prstate.MergeTrialPhaseQueued, prstate.MergeTrialPhaseBuilding,
			prstate.MergeTrialPhaseRunning, prstate.MergeTrialPhaseReviewing,
			prstate.MergeTrialPhaseStacking, prstate.MergeTrialPhasePublishing:
			return true, nil
		}
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
	_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS update_restart_guard (
id INTEGER PRIMARY KEY CHECK(id=1), last_restart INTEGER NOT NULL, busy_since INTEGER NOT NULL, noticed INTEGER NOT NULL)`)
	if err != nil {
		return false, err
	}
	_, err = db.ExecContext(ctx, "INSERT OR IGNORE INTO update_restart_guard VALUES(1,0,0,0)")
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
			if _, err = db.ExecContext(ctx, "UPDATE update_restart_guard SET busy_since=? WHERE id=1", since); err != nil {
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
			if _, err = db.ExecContext(ctx, "UPDATE update_restart_guard SET noticed=1 WHERE id=1"); err != nil {
				return false, err
			}
		}
		return false, nil
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
		Event: room.AlertEvent{Source: room.AlertEngine, Summary: "An update has waited 3 hours for CI to finish.", Detail: "Active merge trials or local CI keep the engine update waiting. Choose a quiet time or run Updates manually."},
	})
	return err
}

func (s Service) recordUpdateRestart(ctx context.Context, now time.Time) error {
	db, err := s.Jobs.db()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "UPDATE update_restart_guard SET last_restart=?,busy_since=0,noticed=0 WHERE id=1", now.Unix())
	return err
}
