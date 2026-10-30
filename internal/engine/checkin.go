package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/automations"
	"dyna-agent/internal/configsvc"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/parentcheckin"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store"
	"dyna-agent/internal/updaterservice"
)

func (s services) setSetting(ctx context.Context, key, value string) (configsvc.Item, error) {
	if key == "updater.source" {
		if err := authority.Check("settings set", authority.Of(ctx), nil); err != nil {
			return configsvc.Item{}, err
		}
		value = strings.TrimSpace(value)
		if value != "" && !filepath.IsAbs(value) {
			return configsvc.Item{}, fmt.Errorf("updater.source must be an absolute path")
		}
		err := (updaterservice.Store{DataDir: store.DataDir()}).Update(ctx, func(c *updaterservice.Config) error {
			if c.Source != value {
				c.ToolPath = ""
			}
			c.Source = value
			c.MultipleHooks = false
			if value == "" {
				c.Enabled = false
			}
			return nil
		})
		if err != nil {
			return configsvc.Item{}, err
		}
		return updateSourceItem(value), nil
	}
	if key == settings.KeySessionCheckInInterval {
		// The hook owns check-ins now, and this value was imported once. A silent
		// write here would look like it changed the cadence, so name the hook.
		return configsvc.Item{}, fmt.Errorf("%s is read once, when the parent check-ins hook is first seeded; change the interval on Settings > Hooks, or with dyna git hooks edit parent-checkins --global --every-minutes <n>", key)
	}
	return configsvc.Set(ctx, key, value)
}

// RunParentCheckIns runs one parent check-in pass with the interval the parent
// check-ins hook owns, and reports how many it posted. A turn end can make a
// check-in due at once, and the hook only runs on its schedule, so the engine
// runs one pass then. ended names the sessions that just ended a turn: a child
// with open work and nothing in flight makes its parent due now. A switched-off
// hook runs no pass: the hook owns check-ins, so its switch stops them everywhere.
func RunParentCheckIns(ctx context.Context, rooms *roomsvc.Service, ended []string) (int, time.Time, error) {
	every, err := checkInInterval(ctx)
	if err != nil || every == 0 {
		return 0, time.Time{}, err
	}
	expedite, err := parentcheckin.TurnEndParents(ctx, rooms.Notify, ended)
	if err != nil {
		return 0, time.Time{}, err
	}
	s := parentcheckin.New(rooms.Notify)
	s.Interval = func() (time.Duration, error) { return every, nil }
	s.Expedite = expedite
	return s.Pass(ctx)
}

// checkInInterval reads the interval of the parent check-ins hook. Zero means
// the hook is switched off or not seeded, so no pass runs.
func checkInInterval(ctx context.Context) (time.Duration, error) {
	rows, err := hooks.New().BuiltinHooks(ctx)
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
