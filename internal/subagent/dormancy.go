package subagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/shells"
)

type callerJobKey struct{}

// withCallerJob returns ctx naming the shell job the turn runs in. An empty job
// adds nothing, and a job must have the shell ID shape.
func withCallerJob(ctx context.Context, job string) (context.Context, error) {
	if job == "" {
		return ctx, nil
	}
	if !shells.ValidID(job) {
		return ctx, fmt.Errorf("dormancy: invalid shell job %q", job)
	}
	return context.WithValue(ctx, callerJobKey{}, job), nil
}

// withoutCallerJob drops the shell item for the job the turn runs in. That job
// ends with the turn, so it is not unfinished work. Responsibilities lists only
// jobs the session owns, so another session's job is never dropped. Any other
// live job still keeps the session awake.
func withoutCallerJob(ctx context.Context, items []Responsibility) []Responsibility {
	job, _ := ctx.Value(callerJobKey{}).(string)
	if job == "" {
		return items
	}
	kept := items[:0]
	for _, r := range items {
		if r.Kind == "shell" && r.ID == job {
			continue
		}
		kept = append(kept, r)
	}
	return kept
}

// dormantAfterTurn makes a session dormant when the turn that just ended left
// it no responsibilities. A dormant session runs nothing, and any message wakes
// it; Dyna closes it after 7 dormant days. The session's own turn runner calls
// this, so no caller names a session.
func (m *Manager) dormantAfterTurn(ctx context.Context, ref string, turn int) (Info, error) {
	info, err := m.store().Resolve(ref)
	if err != nil {
		return Info{}, err
	}
	if turn <= 0 {
		return info, errors.New("dormancy requires a positive turn number")
	}
	if info.Status == StatusClosed || info.Meta.Status == StatusDormant || info.Turns != turn {
		return info, nil
	}
	// Reserve the session first. Goal, question, PR, shell and workflow
	// assignment and child creation all refuse a reserved session, so nothing
	// new can arrive between the responsibility check and the commit.
	token := NewID()
	if err = m.reserveFinish(ctx, info.ID, token); err != nil {
		return info, err
	}
	defer m.releaseDormant(info.ID, token)
	items, err := m.Responsibilities(ctx, info)
	if err != nil {
		return info, err
	}
	if len(withoutCallerJob(ctx, items)) > 0 {
		return info, nil
	}
	if m.checkpoint != nil {
		m.checkpoint("dormant-checked")
	}
	committed := false
	err = m.store().withLockContext(ctx, info.ID, func() error {
		meta, err := m.store().ReadMeta(info.ID)
		if err != nil {
			return err
		}
		if !meta.Closed.IsZero() {
			return ErrClosed
		}
		if meta.Closing == nil || meta.Closing.Token != token {
			return errors.New("dormancy reservation changed")
		}
		// An expired reservation no longer fenced off new work, so its check
		// cannot vouch for the session now.
		if !meta.Closing.Active() {
			return errors.New("dormancy check outlasted its reservation")
		}
		commit := func() error {
			meta.Status, meta.Finished, meta.Closing = StatusDormant, time.Now(), nil
			if err := m.store().writeMeta(meta); err != nil {
				return err
			}
			committed = true
			return nil
		}
		if meta.Turns != turn || meta.Transfer.blocksTurns() || meta.Consolidating != "" {
			return nil
		}
		ended, err := m.store().ReadTurn(info.ID, turn)
		if err != nil {
			return err
		}
		if !ended.Done() || ended.RoomHeld || ended.HeldPrompt != "" {
			return nil
		}
		// Fence both queue claims and appended posts through the commit.
		return m.store().withGateLockContext(ctx, info.ID, func(string) error {
			return m.rooms().WithRoomSnapshot(ctx, meta.RoomID, func(room.Record) error {
				pending, err := m.pendingRoomMessages(meta)
				if err != nil {
					return err
				}
				if len(pending) > 0 {
					return nil
				}
				return commit()
			})
		})
	})
	if err != nil {
		return info, err
	}
	if committed {
		m.store().appendEvent(info.ID, map[string]any{"t": "session_dormant"})
	}
	return m.store().Info(info.ID)
}

// reserveFinish marks the session as checked, under the names lock so a child
// start in progress completes first. Close and dormancy both use it.
func (m *Manager) reserveFinish(ctx context.Context, id, token string) error {
	unlock, err := lockNamesContext(ctx, m.store())
	if err != nil {
		return err
	}
	defer unlock()
	return m.store().withLockContext(ctx, id, func() error {
		meta, err := m.store().ReadMeta(id)
		if err != nil {
			return err
		}
		if !meta.Closed.IsZero() {
			return ErrClosed
		}
		if meta.Closing.Active() {
			return errors.New("session has another close or dormancy check; retry after it")
		}
		meta.Closing = &sessionlife.Closing{Token: token, PID: os.Getpid(), At: time.Now()}
		return m.store().writeMeta(meta)
	})
}
