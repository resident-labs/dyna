package subagent

import (
	"context"
	"errors"

	"dyna-agent/internal/profile"
)

// CompactIdle starts a compact-only turn for session id when it is still idle
// after turn and its last model call held at least profile.IdleCompactMinTokens.
// The engine calls it profile.IdleCompactAfter after turn ended. A session that
// closed, went dormant, ran another turn since, or is busy now is left alone,
// and so is a room that is paused.
func (m *Manager) CompactIdle(ctx context.Context, id string, turn int) error {
	info, err := m.store().Inspect(id)
	if err != nil {
		return err
	}
	if info.Status != StatusIdle || info.Turns != turn || !info.Resumable || info.HarnessSession == "" {
		return nil
	}
	profs, err := m.profiles()
	if err != nil {
		return err
	}
	p, ok := profs.Get(info.Profile)
	if !ok || !profile.IdleCompactDue(*p, info.ContextTokens) {
		return nil
	}
	_, err = m.Wake(ctx, id, WakeOptions{RequireIdle: true, compactOnly: true})
	if errors.Is(err, ErrBusy) {
		return nil
	}
	return err
}
