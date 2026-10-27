package engine

import (
	"context"
	"fmt"
	"slices"
	"time"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
)

// scratchFreedReporter posts one typed, quiet machine notice to every room that
// uses the machine: the rooms its CI work may come from, or every room when the
// machine names none. The notice says what the sweep freed.
func scratchFreedReporter(svc *notify.Service) localci.ScratchReporter {
	return func(ctx context.Context, node localci.Node, result localci.ScratchResult) error {
		if svc == nil {
			return nil
		}
		list, err := svc.Rooms.List(false)
		if err != nil {
			return err
		}
		ev := room.MachineScratchEvent{
			Name:       node.Name,
			Folders:    result.Folders,
			Builds:     result.Builds,
			FreedBytes: int(result.FreedBytes),
			Summary: fmt.Sprintf("%s freed: %d idle folders and %d build outputs removed",
				settings.FormatBytes(result.FreedBytes), result.Folders, result.Builds),
			AtMS: int(time.Now().UnixMilli()),
		}
		var first error
		for _, rec := range list {
			if len(node.Rooms) > 0 && !slices.Contains(node.Rooms, rec.ID) {
				continue
			}
			if _, err := svc.Post(ctx, notify.PostInput{
				Event:    ev,
				Sender:   notify.Sender{Kind: notify.SenderOwner, Room: rec.Name},
				Mentions: []string{"@" + rec.Name},
			}); err != nil && first == nil {
				first = fmt.Errorf("scratch notice %s: %w", rec.Name, err)
			}
		}
		return first
	}
}
