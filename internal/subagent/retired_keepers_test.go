package subagent

import (
	"context"
	"testing"
)

func TestRetireKeepersMatchesRoleNotName(t *testing.T) {
	sessions := []Info{
		{Meta: Meta{ID: "old", Role: "keeper"}, Status: StatusIdle},
		{Meta: Meta{ID: "worker", Name: "keeper"}, Status: StatusIdle},
		{Meta: Meta{ID: "closed", Role: "keeper"}, Status: StatusClosed},
	}
	var closed []string
	err := retireGoalKeepers(t.Context(), sessions, func(_ context.Context, id, why string, opts CloseOptions) (Info, error) {
		if why != "goal keeper removed" || !opts.Force || !opts.KeepWorktrees {
			t.Fatalf("unsafe retirement: %s %+v", why, opts)
		}
		closed = append(closed, id)
		return Info{}, nil
	})
	if err != nil || len(closed) != 1 || closed[0] != "old" {
		t.Fatalf("closed %v: %v", closed, err)
	}
}
