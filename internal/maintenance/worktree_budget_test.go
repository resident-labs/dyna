package maintenance

import (
	"context"
	"errors"
	"testing"

	"dyna-agent/internal/worktrees"
)

func TestSlowRepositoryDoesNotStarveLaterRepositories(t *testing.T) {
	oldShare, oldGC := repoShare, gcWorktrees
	t.Cleanup(func() { repoShare, gcWorktrees = oldShare, oldGC })
	repoShare = 0
	var scanned []string
	gcWorktrees = func(ctx context.Context, repo string, _ worktrees.GCOptions) ([]worktrees.Tree, error) {
		scanned = append(scanned, repo)
		if repo == "slow" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return nil, nil
	}
	var result Result
	err := cleanWorktrees(context.Background(), &result, []string{"slow", "fast"}, worktrees.GCOptions{})
	if len(scanned) != 2 || scanned[1] != "fast" {
		t.Fatalf("repositories cleaned = %v, want slow then fast", scanned)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the slow repository's deadline error", err)
	}
	if len(result.Steps) != 3 || result.Steps[0].Name != "worktrees fetch" {
		t.Fatalf("steps = %+v, want fetch, scan and remove timings", result.Steps)
	}
}
