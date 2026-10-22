package subagent

import (
	"context"
	"errors"
	"time"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/state"
)

const inputWait = 30 * time.Second

func inputWaitDeadline() (<-chan time.Time, func()) {
	timer := time.NewTimer(inputWait)
	return timer.C, func() { timer.Stop() }
}

// RunnerWaitInput waits for input or control work for the verified current
// turn. It neither claims input nor creates a request receipt.
func (m *Manager) RunnerWaitInput(ctx context.Context, input InputClaim) (bool, error) {
	id, err := runnerSession(ctx)
	if err != nil {
		return false, err
	}
	if callspec.RequestOf(ctx) != "" {
		return false, errors.New("read-only input waits do not carry request IDs")
	}
	if input.Startup || input.Final || len(input.Covered) != 0 {
		return false, errors.New("input waits accept only live reads")
	}
	if m.InputEvents == nil {
		return false, errors.New("runner input event hub is unavailable")
	}
	deadline := m.inputWaitDeadline
	if deadline == nil {
		deadline = inputWaitDeadline
	}
	timeout, stop := deadline()
	defer stop()
	events, _, unsubscribe := m.InputEvents.Subscribe()
	defer func() {
		unsubscribe()
		if m.checkpoint != nil {
			m.checkpoint("input-wait-end")
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if m.checkpoint != nil {
			m.checkpoint("input-wait-check")
		}
		// ReadFrom returns one room-log page. Probe every available page
		// without advancing the runner's authoritative cursor. Start again
		// from that cursor after an event, since eligibility may have changed.
		probe := input
		var reply ClaimedInput
		for {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			var err error
			reply, err = m.selectInput(ctx, id, probe, true)
			if err != nil {
				return false, err
			}
			if len(reply.Posts) > 0 || reply.Closed {
				return true, nil
			}
			if reply.Cursor == probe.Cursor {
				break
			}
			probe.Cursor = reply.Cursor
		}
		if m.checkpoint != nil {
			m.checkpoint("input-wait-idle")
		}
		for {
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-timeout:
				return false, nil
			case ev, ok := <-events:
				if !ok {
					unsubscribe()
					events, _, unsubscribe = m.InputEvents.Subscribe()
				} else if !inputEvent(ev, id, reply.roomID) {
					continue
				}
			}
			// One canonical read covers the current state behind every queued
			// doorbell. Bound the drain so a busy publisher cannot starve it.
		drain:
			for range hub.Buffer {
				select {
				case _, ok := <-events:
					if !ok {
						unsubscribe()
						events, _, unsubscribe = m.InputEvents.Subscribe()
						break drain
					}
				default:
					break drain
				}
			}
			break
		}
	}
}

func inputEvent(ev hub.Event, id, roomID string) bool {
	if ev.Topic == sessionlife.Topic {
		return ev.Key == id
	}
	if ev.Room != roomID {
		return false
	}
	switch ev.Topic {
	case "posts", "members", "session", "room.paused", "room.resumed":
		return true
	}
	return false
}

// canonicalInputReader pins the entire inherited selection to stored rows.
// Its embedded Store supplies the same decoder as ordinary input reads.
type canonicalInputReader struct {
	*Store
	ctx context.Context
	db  state.Reader
}

func (r canonicalInputReader) ReadMeta(id string) (Meta, error) {
	var meta Meta
	if err := sessionlife.LoadStored(r.ctx, r.db, id, &meta); err != nil {
		return Meta{}, err
	}
	if meta.Room == "" || meta.RoomID == "" {
		return Meta{}, ErrNoRoom
	}
	return meta, nil
}

func (r canonicalInputReader) readTurnRow(id string, n int) ([]byte, string, error) {
	raw, revision, err := storedTurn(r.ctx, r.db, id, n)
	return raw, turnRevision(revision), err
}
