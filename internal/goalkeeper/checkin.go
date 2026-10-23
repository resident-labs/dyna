package goalkeeper

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/rolesession"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// A kept keeper session rotates after this many passes.
const maxKeeperPasses = 20

// Tick runs one keeper step for every due room: stale nudges and queued
// notice delivery. The check-in wake rounds are gone. Session snapshots
// exclude closed history.
func (s *Service) Tick(ctx context.Context) (time.Time, error) {
	st, err := s.Store.Read()
	if err != nil {
		return time.Time{}, err
	}
	ids := make([]string, 0, len(st.Rooms))
	for id := range st.Rooms {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	// One session list serves every room in this tick.
	var sessions []subagent.Info
	listed := false
	list := func() ([]subagent.Info, error) {
		if listed {
			return sessions, nil
		}
		sessions, err = s.sessionState()
		listed = err == nil
		return sessions, err
	}
	now := s.now()
	live, err := list()
	if err != nil {
		return time.Time{}, err
	}
	// next is when the keeper next has work, so the engine wakes it then
	// instead of every minute. A time already past waits checkInRetry.
	var next time.Time
	add := func(at time.Time) {
		if !at.After(now) {
			at = now.Add(checkInRetry)
		}
		if next.IsZero() || at.Before(next) {
			next = at
		}
	}
	var failures []error
	failures = append(failures, s.nudgeStaleHolders(ctx, st, live, now))
	for _, id := range ids {
		if ctx.Err() != nil {
			return next, ctx.Err()
		}
		if s.Notify.Sessions.RoomPaused(id) {
			// A paused room's work waits for its resume, which wakes the keeper.
			continue
		}
		// Queued notices drain first: holder handbacks deliver even in a
		// room with no open goals. A deferred or failed delivery retries
		// after checkInRetry.
		if r := st.Rooms[id]; len(r.Notices) > 0 {
			if err := s.checkNotices(ctx, id); err != nil {
				failures = append(failures, err)
			}
			fresh, err := s.Store.ReadOpen()
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if len(fresh.Room(id).Notices) > 0 {
				add(now.Add(checkInRetry))
			} else if at := nudgeNext(fresh.Room(id), now); !at.IsZero() {
				add(at)
			}
			continue
		}
		if at := nudgeNext(st.Rooms[id], now); !at.IsZero() {
			add(at)
		}
	}
	return next, errors.Join(failures...)
}

// checkInRetry is how soon the keeper looks again at a room with a round or
// queued notices.
const checkInRetry = time.Minute

// roomWork reports whether a room has queued notices to deliver. The
// check-in wake rounds are gone; stale nudges schedule their own ticks.
func roomWork(r *goals.RoomState, now time.Time) (bool, time.Time) {
	if len(r.Notices) > 0 {
		return true, now.Add(checkInRetry)
	}
	return false, time.Time{}
}

// nudgeNext is when the next stale window can elapse, so the engine wakes
// the keeper for the next nudge.
func nudgeNext(r *goals.RoomState, now time.Time) time.Time {
	var next time.Time
	for _, g := range r.Goals {
		if !g.Open() || len(g.Holders) == 0 {
			continue
		}
		at := g.NudgedAt
		if at.IsZero() {
			at = g.UpdatedAt
		}
		at = at.Add(goals.StaleAfter)
		if next.IsZero() || at.Before(next) {
			next = at
		}
	}
	if next.IsZero() {
		return time.Time{}
	}
	if next.Before(now) {
		return now.Add(checkInRetry)
	}
	return next
}

// lookupKeeper finds the room's keeper session by its saved ID or its name.
func (s *Service) lookupKeeper(roomID, id string) rolesession.LookupResult {
	backend := s.backend()
	return rolesession.Lookup(id, roomID, subagent.RoleKeeper, backend.Inspect, func() (subagent.Info, error) { return backend.Named(roomID) })
}

// deliverNotices posts the room's queued notices and removes each one whose
// post is stored. A failed or deferred post stays queued for the next tick.
// Journal notices go strictly in order: a later one waits while an earlier
// one is queued, so rounds append in time order, which AddJournal's retry
// check relies on.
func (s *Service) deliverNotices(ctx context.Context, roomID string, r *goals.RoomState) error {
	if len(r.Notices) == 0 {
		return nil
	}
	rec, err := s.Notify.Rooms.Resolve(roomID)
	if err != nil {
		return err
	}
	delivered := map[string]bool{}
	var failures []error
	for _, n := range r.Notices {
		done, err := s.deliverNotice(ctx, rec, r, n)
		if err != nil {
			failures = append(failures, err)
		}
		delivered[n.ID] = done && err == nil
	}
	err = s.Store.UpdateOpen(func(st *goals.State) error {
		fresh := st.Room(roomID)
		kept := fresh.Notices[:0]
		for _, n := range fresh.Notices {
			if !delivered[n.ID] {
				kept = append(kept, n)
			}
		}
		fresh.Notices = kept
		*r = *fresh
		return nil
	})
	return errors.Join(append(failures, err)...)
}

func (s *Service) deliverNotice(ctx context.Context, rec room.Record, r *goals.RoomState, n goals.Notice) (bool, error) {
	kind := n.Kind
	// TODO: legacy/shim remove later — holder-closed notices queued before notice kinds existed; remove when stored states hold none.
	if kind == "" && n.Intent == "reassigned" {
		kind = "holder_closed"
	}
	switch kind {
	case "holder_closed", "item_moved":
		if kind == "holder_closed" {
			if g := r.Goals[n.Goal]; g == nil || !g.Open() {
				return true, nil
			}
		}
		text := "Goal " + n.Goal + ": " + n.Text
		if n.To != "" {
			info, err := s.Notify.Sessions.Store.Resolve(n.To)
			if err != nil && kind == "item_moved" {
				return false, err
			}
			if err == nil && info.Status != subagent.StatusClosed {
				input := room.PostInput{Wake: true, ID: stableID(rec.ID + "/" + n.ID), From: room.GoalKeeperSender, Kind: "goal", Text: text,
					To: []string{info.Address()}, Via: []string{"@" + info.Name}, SessionIDs: map[string]string{info.Address(): info.ID}}
				return s.Notify.Sessions.DeliverGoal(ctx, info.ID, func() (string, error) { msg, err := s.Notify.Rooms.Post(rec.ID, input); return msg.ID, err })
			}
		}
		return true, s.postToRoot(rec, true, room.PostInput{ID: stableID(rec.ID + "/" + n.ID), Kind: "goal", Text: text})
	case "journal", "close", "goalless":
		// TODO: legacy/shim remove later — known check-in notice kinds retire without delivery; remove when stored states hold none.
		return true, nil
	default:
		return false, fmt.Errorf("unsupported goal notice kind %q", kind)
	}
}

// postToRoot sends a keeper post to root's session and wakes it when wake is
// true. Otherwise, or when root is closed, the post goes quietly to the owner.
func (s *Service) postToRoot(rec room.Record, wake bool, input room.PostInput) error {
	input.From, input.To = room.GoalKeeperSender, []string{rec.Name}
	if wake {
		root, ok, err := s.Notify.Sessions.RootSession(rec.ID)
		if err != nil {
			return err
		}
		if ok && root.Status != subagent.StatusClosed {
			input.Wake, input.To, input.SessionIDs = true, []string{root.Address()}, map[string]string{root.Address(): root.ID}
		}
	}
	_, err := s.Notify.Rooms.Post(rec.ID, input)
	return err
}

// checkNotices drains a room's queued notices.
func (s *Service) checkNotices(ctx context.Context, roomID string) error {
	lock, err := rolesession.Lock(s.Store.Root, "keeper", roomID)
	if err != nil {
		return fmt.Errorf("keeper room assignment is busy: %w", err)
	}
	defer rolesession.Unlock(lock)
	st, err := s.Store.ReadOpen()
	if err != nil {
		return err
	}
	r := st.Room(roomID)
	if len(r.Notices) == 0 {
		return nil
	}
	return s.deliverNotices(ctx, roomID, r)
}
