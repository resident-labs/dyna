package engine

import (
	"context"
	"fmt"
	"time"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
)

// machineDiskAlerter tells each room's root that a machine's disk stopped
// admitting new work. The machines registry is not a room's, so every room's
// root hears it once: the owner reads the alert where they read their work.
type machineDiskAlerter struct{ notify *notify.Service }

// MachineLowDisk posts the one low-disk alert of a machine whose disk started
// to block new work, with the words the queue itself returns. It posts a typed
// event, so the feed draws a card and no raw text.
func (a machineDiskAlerter) MachineLowDisk(ctx context.Context, name, block string, free uint64) error {
	if a.notify == nil {
		return nil
	}
	list, err := a.notify.Rooms.List(false)
	if err != nil {
		return err
	}
	var first error
	for _, rec := range list {
		ev := room.MachineEvent{
			Name:      name,
			FreeBytes: int(free),
			Block:     block,
			AtMS:      int(time.Now().UnixMilli()),
		}
		if _, err := a.notify.Post(ctx, notify.PostInput{
			NeedsRoot: true,
			Event:     ev,
			Sender:    notify.Sender{Kind: notify.SenderOwner, Room: rec.Name},
			Mentions:  []string{"@" + rec.Name},
		}); err != nil && first == nil {
			first = fmt.Errorf("alert %s: %w", rec.Name, err)
		}
	}
	return first
}
