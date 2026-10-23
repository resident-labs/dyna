package engine

import (
	"context"
	"time"

	"dyna-agent/internal/configsvc"
	"dyna-agent/internal/parentcheckin"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/settings"
)

func (s services) setSetting(ctx context.Context, key, value string) (configsvc.Item, error) {
	item, err := configsvc.Set(ctx, key, value)
	if err == nil && item.Key == settings.KeySessionCheckInInterval && s.settingsChanged != nil {
		s.settingsChanged()
	}
	return item, err
}

// RunParentCheckIns runs one parent check-in pass: it posts every check-in that
// is due, reports how many it posted, and when the next one is due. It is the
// engine's one entry point for a pass: the check-ins worker times its next pass
// with it, and the built-in parent check-ins job reports the count.
func RunParentCheckIns(ctx context.Context, rooms *roomsvc.Service) (int, time.Time, error) {
	return parentcheckin.New(rooms.Notify).Pass(ctx)
}
