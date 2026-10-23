package engine

import (
	"context"
	"time"

	"dyna-agent/internal/automations"
	"dyna-agent/internal/configsvc"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/parentcheckin"
	"dyna-agent/internal/roomsvc"
)

func (s services) setSetting(ctx context.Context, key, value string) (configsvc.Item, error) {
	return configsvc.Set(ctx, key, value)
}

// RunParentCheckIns runs one parent check-in pass with the interval the parent
// check-ins hook owns, and reports how many it posted. A turn end can make a
// check-in due at once, and the hook only runs on its schedule, so the engine
// runs one pass then. A switched-off hook runs no pass: the hook owns
// check-ins, so its switch stops them everywhere.
func RunParentCheckIns(ctx context.Context, rooms *roomsvc.Service) (int, time.Time, error) {
	every, err := checkInInterval(ctx)
	if err != nil || every == 0 {
		return 0, time.Time{}, err
	}
	s := parentcheckin.New(rooms.Notify)
	s.Interval = func() (time.Duration, error) { return every, nil }
	return s.Pass(ctx)
}

// checkInInterval reads the interval of the parent check-ins hook. Zero means
// the hook is switched off or not seeded, so no pass runs.
func checkInInterval(ctx context.Context) (time.Duration, error) {
	rows, err := hooks.New().ListGlobal(ctx)
	if err != nil {
		return 0, err
	}
	for _, h := range rows {
		if h.Builtin != automations.BuiltinParentCheckIns {
			continue
		}
		if h.Disabled {
			return 0, nil
		}
		return time.Duration(h.EveryMinutes) * time.Minute, nil
	}
	return 0, nil
}
