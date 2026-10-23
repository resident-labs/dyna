package subagent

import (
	"context"
	"errors"
	"sync"
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
	meta, err := storedMetas.load(r.ctx, r.db, r.root(), id)
	if err != nil {
		return Meta{}, err
	}
	if meta.Room == "" || meta.RoomID == "" {
		return Meta{}, ErrNoRoom
	}
	return meta, nil
}

// storedMetas keeps each waiting session's last decoded record, keyed by the
// store root and the row's revision. A wake that follows no session write
// then reads one integer instead of loading and decoding the whole record.
// Callers must not modify the returned Meta's slices or maps. It holds at
// most metaCacheSize records: a session that stops waiting leaves its entry
// until a newer one displaces it.
var storedMetas metaCache

const metaCacheSize = 256

type metaCache struct {
	mu   sync.Mutex
	byID map[[2]string]versionedMeta
}

type versionedMeta struct {
	version sessionlife.Version
	meta    Meta
}

func (c *metaCache) load(ctx context.Context, db state.Reader, root, id string) (Meta, error) {
	key := [2]string{root, id}
	version, err := sessionlife.StoredVersion(ctx, db, id)
	if err != nil {
		return Meta{}, err
	}
	c.mu.Lock()
	hit, ok := c.byID[key]
	c.mu.Unlock()
	if ok && hit.version == version {
		return hit.meta, nil
	}
	var meta Meta
	if version, err = sessionlife.LoadStoredVersion(ctx, db, id, &meta); err != nil {
		return Meta{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !meta.Closed.IsZero() {
		delete(c.byID, key)
		return meta, nil
	}
	if c.byID == nil {
		c.byID = map[[2]string]versionedMeta{}
	}
	if _, kept := c.byID[key]; !kept && len(c.byID) >= metaCacheSize {
		for old := range c.byID {
			delete(c.byID, old)
			break
		}
	}
	c.byID[key] = versionedMeta{version, meta}
	return meta, nil
}

func (r canonicalInputReader) readTurnRow(id string, n int) ([]byte, string, error) {
	raw, revision, err := storedTurn(r.ctx, r.db, id, n)
	return raw, turnRevision(revision), err
}
