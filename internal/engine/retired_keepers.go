package engine

import (
	"context"
	"errors"
	"fmt"

	"dyna-agent/internal/subagent"
)

// retireGoalKeepers closes the removed service before session workers resume.
// It matches the stored role, so an ordinary session named keeper is untouched.
func retireGoalKeepers(ctx context.Context, sessions []subagent.Info, closeSession func(context.Context, string, string, subagent.CloseOptions) (subagent.Info, error)) error {
	var failures []error
	for _, info := range sessions {
		if info.Role != "keeper" || info.Status == subagent.StatusClosed {
			continue
		}
		if _, err := closeSession(ctx, info.ID, "goal keeper removed", subagent.CloseOptions{Force: true, KeepWorktrees: true}); err != nil {
			failures = append(failures, fmt.Errorf("retire keeper %s: %w", info.ID, err))
		}
	}
	return errors.Join(failures...)
}
