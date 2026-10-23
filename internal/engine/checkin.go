package engine

import (
	"context"
	"dyna-agent/internal/configsvc"
	"dyna-agent/internal/settings"
)

func (s services) setSetting(ctx context.Context, key, value string) (configsvc.Item, error) {
	item, err := configsvc.Set(ctx, key, value)
	if err == nil && item.Key == settings.KeySessionCheckInInterval && s.settingsChanged != nil {
		s.settingsChanged()
	}
	return item, err
}
