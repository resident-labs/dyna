package engine

import (
	"context"
	"testing"

	"dyna-agent/internal/subagent"
)

func TestRetireKeepersMatchesRoleNotName(t *testing.T) {
	sessions := []subagent.Info{
		{Meta: subagent.Meta{ID: "old", Role: "keeper"}, Status: subagent.StatusIdle},
		{Meta: subagent.Meta{ID: "worker", Name: "keeper"}, Status: subagent.StatusIdle},
		{Meta: subagent.Meta{ID: "closed", Role: "keeper"}, Status: subagent.StatusClosed},
	}
	var closed []string
	err := retireGoalKeepers(t.Context(), sessions, func(_ context.Context, id, why string, opts subagent.CloseOptions) (subagent.Info, error) {
		if why != "goal keeper removed" || !opts.Force || !opts.KeepWorktrees {
			t.Fatalf("unsafe retirement: %s %+v", why, opts)
		}
		closed = append(closed, id)
		return subagent.Info{}, nil
	})
	if err != nil || len(closed) != 1 || closed[0] != "old" {
		t.Fatalf("closed %v: %v", closed, err)
	}
}
