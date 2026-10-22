package engine

import (
	"context"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/lanesync"
	"dyna-agent/internal/settings"
)

// syncFetch uses the engine's configured repositories. The request cannot
// select another caller or add repositories outside that configuration.
func (s services) syncFetch(ctx context.Context) (lanesync.Status, error) {
	if err := authority.Check("git sync fetch", authority.Of(ctx), nil); err != nil {
		return nil, err
	}
	v, err := settings.Load("")
	if err != nil {
		return nil, err
	}
	if err := s.sync.Fetch(ctx, v.LocalLaneRepos); err != nil {
		return nil, err
	}
	return s.sync.Status()
}
