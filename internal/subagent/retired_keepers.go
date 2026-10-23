package subagent

import (
	"context"
	"errors"
	"fmt"
)

// RetireGoalKeepers closes obsolete keeper services before workers resume.
func (m *Manager) RetireGoalKeepers(ctx context.Context) error {
	sessions, err := m.Store.ListSnapshot(false)
	if err != nil {
		return err
	}
	return retireGoalKeepers(ctx, sessions, m.CloseForEngine)
}

// retireGoalKeepers closes the removed service before session workers resume.
// It matches the stored role, so an ordinary session named keeper is untouched.
func retireGoalKeepers(ctx context.Context, sessions []Info, closeSession func(context.Context, string, string, CloseOptions) (Info, error)) error {
	var failures []error
	for _, info := range sessions {
		if info.Role != "keeper" || info.Status == StatusClosed {
			continue
		}
		if _, err := closeSession(ctx, info.ID, "goal keeper removed", CloseOptions{Force: true, KeepWorktrees: true}); err != nil {
			failures = append(failures, fmt.Errorf("retire keeper %s: %w", info.ID, err))
		}
	}
	return errors.Join(failures...)
}
