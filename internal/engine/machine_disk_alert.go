package engine

import (
	"context"
	"fmt"
	"time"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
)

// machineDiskAlerter tells each room's root that a machine stopped taking CI
// work. The machines registry is not a room's, so every room's root hears it
// once: the owner reads the alert where they read their work.
type machineDiskAlerter struct{ notify *notify.Service }

// MachineLowDisk posts the one low-disk alert of a machine that started holding
// CI. It posts a typed event, so the feed draws a card and no raw text.
func (a machineDiskAlerter) MachineLowDisk(ctx context.Context, name string, free, floor uint64) error {
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
			Name:       name,
			FreeBytes:  int(free),
			FloorBytes: int(floor),
			Summary:    fmt.Sprintf("%s free, needs %s", settings.FormatBytes(int64(free)), settings.FormatBytes(int64(floor))),
			AtMS:       int(time.Now().UnixMilli()),
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
