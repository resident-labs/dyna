package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"dyna-agent/internal/agentslice"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/room"
)

// wireRoomLimitsNotice teaches agentslice to tell a room, through one typed
// notice, that this machine cannot give each room its own resource slice.
// The launch path must never wait on a room post, so the notice posts on its
// own goroutine; the event's Once rule keeps one card per room and cause even
// across engine restarts.
func wireRoomLimitsNotice(ctx context.Context, svc *notify.Service) {
	if svc == nil || svc.Rooms == nil {
		return
	}
	agentslice.RoomLimitsNotice = func(roomID, reason string) {
		go func() {
			if err := postRoomLimitsNotice(ctx, svc, roomID, reason); err != nil {
				log.Print(watch.Sanitize("room limits notice: " + err.Error()))
			}
		}()
	}
}

// wireRoomLimitsLookup teaches agentslice each room's saved limits, so the
// launch path writes them into the room's slice without agentslice reading
// the room store. A missing room keeps the machine defaults.
func wireRoomLimitsLookup(store *room.Store) {
	if store == nil {
		return
	}
	agentslice.RoomLimitsLookup = func(roomID string) (agentslice.RoomLimitsSetting, bool) {
		rec, err := store.Resolve(roomID)
		if err != nil {
			return agentslice.RoomLimitsSetting{}, false
		}
		return agentslice.RoomLimitsSetting{
			MemoryHigh: limitOverride(rec.Limits.MemoryHigh),
			MemoryMax:  limitOverride(rec.Limits.MemoryMax),
			TasksMax:   limitOverride(rec.Limits.TasksMax),
		}, true
	}
}

// limitOverride is the room record's limit as agentslice reads it.
func limitOverride(l room.Limit) agentslice.RoomLimitOverride {
	return agentslice.RoomLimitOverride{Off: l.Off, Value: l.Value}
}

// postRoomLimitsNotice stores the machine's limits-off event in the room that
// hit the limit. It is quiet: no session is woken, and the card explains what
// the room lost and why. The event's Once rule keeps one card per room and
// cause, because the post id derives from the event, not the clock.
func postRoomLimitsNotice(ctx context.Context, svc *notify.Service, roomID, reason string) error {
	rec, err := svc.Rooms.Resolve(roomID)
	if err != nil {
		return fmt.Errorf("resolve room %s: %w", roomID, err)
	}
	if _, err := svc.Rooms.PostEventInput(room.PostEventInput{
		RoomID: rec.ID,
		Event:  room.MachineLimitsEvent{Reason: reason, AtMS: int(time.Now().UnixMilli())},
		To:     []string{rec.Name},
	}); err != nil {
		return errors.Join(fmt.Errorf("post to %s", rec.Name), err)
	}
	return nil
}
