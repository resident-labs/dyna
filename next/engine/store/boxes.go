package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// BoxWant is the durable desired lifecycle state of a box.
type BoxWant string

const (
	WantRun     BoxWant = "run"
	WantStop    BoxWant = "stop"
	WantClose   BoxWant = "close"
	WantDiscard BoxWant = "discard"
)

// LifecycleBox combines observed and desired state for reconciliation.
type LifecycleBox struct {
	box.Box
	Want      BoxWant
	DueAt     time.Time
	Failures  int
	RetryHost contract.HostName // nonempty consumes the one-other-host budget and excludes this first failed host
	Paused    bool
	Deleting  bool
	Limits    contract.RoomLimits
	Only      []string // its room's rule
}

func (s *Store) Boxes(ctx context.Context) ([]box.Box, error) {
	rows, err := s.lifecycleBoxes(ctx, false)
	if err != nil {
		return nil, err
	}
	var boxes []box.Box
	for _, row := range rows {
		boxes = append(boxes, row.Box)
	}
	return boxes, nil
}

func (s *Store) LifecycleBoxes(ctx context.Context) ([]LifecycleBox, error) {
	return s.lifecycleBoxes(ctx, true)
}

func (s *Store) lifecycleBoxes(ctx context.Context, activeOnly bool) ([]LifecycleBox, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT id,session,host,image,size,state,reason,slice,created_at,updated_at,want,due_at,failures,create_retry_host,gpu_gib,gpu_index,(SELECT room FROM sessions WHERE id=boxes.session),(SELECT r.cpus FROM rooms r JOIN sessions s ON s.room=r.id WHERE s.id=boxes.session),(SELECT r.mem_bytes FROM rooms r JOIN sessions s ON s.room=r.id WHERE s.id=boxes.session),EXISTS(SELECT 1 FROM sessions s JOIN rooms r ON r.id=s.room WHERE s.id=boxes.session AND r.paused_at IS NOT NULL),EXISTS(SELECT 1 FROM sessions s JOIN rooms r ON r.id=s.room WHERE s.id=boxes.session AND r.state='deleting'),(SELECT r.only_hosts FROM rooms r JOIN sessions s ON s.room=r.id WHERE s.id=boxes.session) FROM boxes WHERE NOT ? OR NOT (state='deleted' AND EXISTS (SELECT 1 FROM sessions WHERE sessions.id=boxes.session AND sessions.closed_at IS NOT NULL)) ORDER BY (SELECT created_at FROM sessions WHERE id=boxes.session),id", activeOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var boxes []LifecycleBox
	for rows.Next() {
		var b LifecycleBox
		var host sql.NullString
		var size contract.BoxSizeName
		var created, updated, due int64
		var only string
		if err := rows.Scan(&b.ID, &b.Session, &host, &b.Image, &size, &b.State, &b.Reason, &b.Slice, &created, &updated, &b.Want, &due, &b.Failures, &b.RetryHost, &b.GPU, &b.GPUCard, &b.Room, &b.Limits.CPUs, &b.Limits.MemBytes, &b.Paused, &b.Deleting, &only); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(only), &b.Only); err != nil {
			return nil, err
		}
		b.Host = contract.HostName(host.String)
		var ok bool
		b.Size, ok = box.Size(size)
		if !ok {
			return nil, box.TransitionError{From: b.State, To: b.State}
		}
		if due != 0 {
			b.DueAt = time.UnixMilli(due)
		}
		b.PlacedAt = time.UnixMilli(created)
		b.Updated = time.UnixMilli(updated)
		boxes = append(boxes, b)
	}
	return boxes, rows.Err()
}

func (s *Store) Box(ctx context.Context, id contract.BoxID) (box.Box, error) {
	boxes, err := s.Boxes(ctx)
	if err != nil {
		return box.Box{}, err
	}
	for _, b := range boxes {
		if b.ID == id {
			return b, nil
		}
	}
	return box.Box{}, sql.ErrNoRows
}

// TransitionBox changes the record and publishes its event in one transaction.
func (s *Store) TransitionBox(ctx context.Context, b box.Box, to contract.BoxState, reason string, event contract.Event) (box.Box, error) {
	next, body, err := box.Transition(b, to, reason, event.Time)
	if err != nil {
		return box.Box{}, err
	}
	body.Cause = event.Body.(contract.BoxChanged).Cause
	event.Body = body
	err = s.Write(ctx, func(tx *sql.Tx) error {
		var host *contract.HostName
		if next.Host != "" {
			host = &next.Host
		}
		result, err := tx.ExecContext(ctx, "UPDATE boxes SET host=?,image=?,size=?,state=?,reason=?,slice=?,updated_at=?,due_at=CASE WHEN ?='ready' THEN 0 ELSE due_at END WHERE id=? AND state=? AND (want='run' OR ?!='ready') AND (?!='ready' OR ? NOT IN ('creating','starting') OR due_at=0 OR due_at>?) AND (?!='lost' OR ?!='ready' OR EXISTS (SELECT 1 FROM hosts WHERE name=boxes.host AND state!='unreachable' AND state!='removed'))", host, next.Image, next.Size.Name, next.State, next.Reason, next.Slice, event.Time.UnixMilli(), next.State, next.ID, b.State, next.State, next.State, b.State, event.Time.UnixMilli(), b.State, next.State)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return sql.ErrNoRows
		}
		_, err = s.AppendEvent(tx, event)
		return err
	})
	return next, err
}

// WakeLostInventory schedules inventory; an in-flight check may defer it by up to 60 seconds.
func (s *Store) WakeLostInventory(ctx context.Context, id contract.BoxID) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE boxes SET due_at=0 WHERE id=? AND state='lost' AND due_at!=0`, id)
		return err
	})
}

// SetBoxLifecycle commits an observed transition and retry data together.
// A stale observed state returns false without publishing an event.
func (s *Store) SetBoxLifecycle(ctx context.Context, old, next box.Box, due time.Time, failures int, event contract.Event) (bool, error) {
	changed := old.State != next.State || old.Reason != next.Reason
	if old.State != next.State {
		_, body, err := box.Transition(old, next.State, next.Reason, event.Time)
		if err != nil {
			return false, err
		}
		body.Host = next.Host
		body.Cause = event.Body.(contract.BoxChanged).Cause
		event.Body = body
	} else if changed {
		if old.State != contract.BoxPending && old.State != contract.BoxStarting {
			return false, box.TransitionError{From: old.State, To: next.State}
		}
		cause := event.Body.(contract.BoxChanged).Cause
		event.Body = contract.BoxChanged{Box: old.ID, Session: old.Session, Host: next.Host, From: old.State, To: next.State, Reason: next.Reason, Cause: cause}
	}
	var dueMS int64
	if !due.IsZero() {
		dueMS = due.UnixMilli()
	}
	var updated bool
	err := s.Write(ctx, func(tx *sql.Tx) error {
		var host *contract.HostName
		if next.Host != "" {
			host = &next.Host
		}
		admission := (old.State == contract.BoxPending || old.State == contract.BoxStopped) && next.State != contract.BoxClosing && next.State != contract.BoxStopped
		// created_at is the allocation time for disk accounting, not the waiter's enqueue time.
		result, err := tx.ExecContext(ctx, `UPDATE boxes SET host=?,image=?,size=?,state=?,reason=?,slice=?,gpu_index=?,created_at=CASE WHEN state='pending' AND ?='creating' AND image='' THEN ? ELSE created_at END,updated_at=?,due_at=?,failures=?,create_generation=CASE WHEN ?='creating' THEN coalesce((SELECT create_generation FROM hosts WHERE name=?),0) ELSE create_generation END WHERE id=? AND state=? AND (? NOT IN ('starting','ready') OR want='run') AND (NOT ? OR (want='run' AND due_at<=?)) AND (?!='creating' OR image!='' OR EXISTS (SELECT 1 FROM hosts WHERE name=? AND create_problem='')) AND (?!='lost' OR ?='closing' OR EXISTS (SELECT 1 FROM hosts WHERE name=boxes.host AND state='ready')) AND (?!='lost' OR ?!='ready' OR want IN ('run','stop'))`, host, next.Image, next.Size.Name, next.State, next.Reason, next.Slice, next.GPUCard, next.State, event.Time.UnixMilli(), event.Time.UnixMilli(), dueMS, failures, next.State, host, old.ID, old.State, next.State, admission, event.Time.UnixMilli(), next.State, host, old.State, next.State, old.State, next.State)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		updated = true
		if changed {
			_, err = s.AppendEvent(tx, event)
		}
		return err
	})
	return updated, err
}

// QuarantineShare keeps session boxes off the host that a create just found without its shared folder, as the host check does
// for a host that cannot mount it. It is for a box that built nothing there: a box with files on the host is not moved. Boxes
// waiting there that built nothing place elsewhere, and the failed box detaches on restart.
func (s *Store) QuarantineShare(ctx context.Context, b box.Box) error {
	if b.Image != "" {
		return nil
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE hosts SET create_problem=?1 WHERE name=?2 AND create_problem='' AND create_generation=(SELECT create_generation FROM boxes WHERE id=?3)`, contract.HostProblemShareUnavailable, b.Host, b.ID)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n == 0 {
			return err
		}
		return detachWaitingCreates(ctx, tx, b.Host)
	})
}

// FinishCreate records the host result without overwriting a concurrent hello.
func (s *Store) FinishCreate(ctx context.Context, b box.Box, image, failure string, due time.Time, failures int, event contract.Event, runtimeFailure bool) error {
	finished, ok := event.Body.(contract.BoxChanged)
	if !ok {
		return fmt.Errorf("finish create of box %s: event body is %T, not a box change", b.ID, event.Body)
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		var state contract.BoxState
		var savedHost, retryHost contract.HostName
		var savedImage string
		if err := tx.QueryRowContext(ctx, `SELECT state,coalesce(host,''),image,create_retry_host FROM boxes WHERE id=?`, b.ID).Scan(&state, &savedHost, &savedImage, &retryHost); err != nil {
			if err == sql.ErrNoRows {
				return nil
			}
			return err
		}
		if savedHost != b.Host || state != contract.BoxCreating && state != contract.BoxStarting && state != contract.BoxReady {
			return nil
		}
		b.State = state
		cause := finished.Cause
		to := state
		reason := ""
		if failure != "" {
			image = b.Image
			if state != contract.BoxCreating || failures >= 3 {
				to, reason = contract.BoxFailed, string(contract.CauseCreateFailed)
				if failures >= 3 {
					reason = failure // the step may know more, as for a compose file that cannot run
				}
			} else {
				to, reason = contract.BoxPending, string(box.PendingCreateRetry)
			}
		} else if state == contract.BoxCreating {
			to = contract.BoxStarting
		}
		if to == contract.BoxFailed && cause == "" {
			cause = contract.CauseCreateFailed
		}
		if failure != "" && savedImage != "" {
			image = savedImage
		}
		terminalCreateFailure := to == contract.BoxFailed && cause == contract.CauseCreateFailed
		safeState := savedHost != "" && state == contract.BoxCreating && savedImage == ""
		eligibility := createRetryUnknown
		if terminalCreateFailure && retryHost == "" && safeState {
			var err error
			eligibility, err = allocationRetryEligibility(ctx, tx, b.ID, event.Room)
			if err != nil {
				return err
			}
		}
		retry := terminalCreateFailure && retryHost == "" && safeState && eligibility == createRetryUnbound
		if retry {
			to, reason, image = contract.BoxPending, string(box.PendingOtherHostRetry), ""
			failures = 0
			due = time.Time{}
		}
		if terminalCreateFailure && !retry && retryHost == "" && (!safeState || eligibility == createRetryUsed) {
			reason = failure + "; automatic other-host retry unsafe: allocation already started or has session work"
		}
		if to != state {
			_, body, err := box.Transition(b, to, reason, event.Time)
			if err != nil {
				return err
			}
			body.Cause, body.Phase = cause, finished.Phase
			if retry {
				body.Cause, body.Host = "", ""
			}
			if to == contract.BoxFailed && body.Cause == "" {
				body.Cause = contract.CauseCreateFailed
			}
			event.Body = body
		}
		dueMS := int64(0)
		if (to == contract.BoxStarting || to == contract.BoxPending) && !due.IsZero() {
			dueMS = due.UnixMilli()
		}
		// A completed turn after an early hello may have reset the current failure count.
		result, err := tx.ExecContext(ctx, `UPDATE boxes SET host=CASE WHEN ? THEN NULL ELSE host END,create_retry_host=CASE WHEN ? THEN ? ELSE create_retry_host END,failed_create=CASE WHEN ?='failed' AND ?!='' THEN 1 ELSE 0 END,image=?,state=?,reason=?,due_at=?,created_at=CASE WHEN ?='' THEN ? ELSE created_at END,failures=CASE WHEN ?='ready' THEN failures ELSE ? END WHERE id=? AND state=? AND want='run'`, retry, retry, b.Host, to, failure, image, to, reason, dueMS, failure, event.Time.UnixMilli(), state, failures, b.ID, state)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		if retry {
			_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO room_leftovers(host,room,box,slice) SELECT ?,s.room,b.id,b.slice FROM boxes b JOIN sessions s ON s.id=b.session WHERE b.id=?`, b.Host, b.ID)
			if err != nil {
				return err
			}
		}
		// Count completed failed creations, not their retry attempts. The generation fences
		// a result that was already in flight when the owner cleared the problem.
		if failure == "" || terminalCreateFailure && runtimeFailure {
			_, err = tx.ExecContext(ctx, `UPDATE hosts SET
				create_failures=CASE WHEN ?='' THEN 0 ELSE create_failures+1 END,
				create_problem=CASE WHEN ?='' THEN CASE WHEN create_problem='create_failed' THEN '' ELSE create_problem END WHEN create_failures+1>=3 THEN 'create_failed' ELSE create_problem END
				WHERE name=? AND create_generation=(SELECT create_generation FROM boxes WHERE id=?)`, failure, failure, b.Host, b.ID)
			if err != nil {
				return err
			}
			if failure != "" {
				var quarantined bool
				if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hosts WHERE name=? AND create_problem!='')`, b.Host).Scan(&quarantined); err != nil {
					return err
				}
				if quarantined {
					if err := detachWaitingCreates(ctx, tx, b.Host); err != nil {
						return err
					}
				}
			}
		}
		if to != state {
			_, err = s.AppendEvent(tx, event)
		}
		return err
	})
}
