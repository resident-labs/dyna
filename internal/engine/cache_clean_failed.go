package engine

import (
	"context"
	"fmt"
	"slices"
	"time"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
)

// cacheCleanFailedReporter posts one typed machine alert to every room that
// uses the machine. The event carries one path and one bounded reason, so the
// engine log and the room never receive the container runtime's output flood.
func cacheCleanFailedReporter(svc *notify.Service) localci.CacheCleanReporter {
	return func(ctx context.Context, node localci.Node, failure localci.CacheCleanError) error {
		if svc == nil {
			return nil
		}
		list, err := svc.Rooms.List(false)
		if err != nil {
			return err
		}
		ev := room.MachineCacheCleanFailedEvent{
			Name:          node.Name,
			Path:          failure.Path,
			Reason:        failure.Reason,
			LastSuccessMS: failure.LastSuccessMS,
			AtMS:          int(time.Now().UnixMilli()),
		}
		var first error
		for _, rec := range list {
			if len(node.Rooms) > 0 && !slices.Contains(node.Rooms, rec.ID) {
				continue
			}
			if _, err := svc.Post(ctx, notify.PostInput{
				ID:        room.EventID(ev),
				NeedsRoot: true,
				Event:     ev,
				Sender:    notify.Sender{Kind: notify.SenderOwner, Room: rec.Name},
				Mentions:  []string{"@" + rec.Name},
			}); err != nil && first == nil {
				first = fmt.Errorf("cache cleanup notice %s: %w", rec.Name, err)
			}
		}
		return first
	}
}
