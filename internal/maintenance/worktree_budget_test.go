package maintenance

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"dyna-agent/internal/store"
	"dyna-agent/internal/worktrees"
)

func TestRunStepGivesLaterStepTimeAfterSlowStep(t *testing.T) {
	var result Result
	slowErr := runStep(context.Background(), &result, "slow", 0, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	laterRan := false
	laterErr := runStep(context.Background(), &result, "later", time.Hour, func(ctx context.Context) error {
		laterRan = ctx.Err() == nil
		return nil
	})
	if !errors.Is(slowErr, context.DeadlineExceeded) {
		t.Fatalf("slow step err = %v, want its share's deadline", slowErr)
	}
	if laterErr != nil || !laterRan {
		t.Fatalf("later step ran = %v, err = %v; want it to run with time left", laterRan, laterErr)
	}
	if len(result.Steps) != 2 || result.Steps[0].Name != "slow" || result.Steps[1].Name != "later" {
		t.Fatalf("steps = %+v, want slow then later", result.Steps)
	}
}

func TestRunStepSkipsStepsAfterExternalCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var result Result
	if err := runStep(ctx, &result, "first", time.Hour, func(context.Context) error {
		cancel()
		return nil
	}); err != nil {
		t.Fatalf("first step err = %v, want nil", err)
	}
	ran := false
	err := runStep(ctx, &result, "second", time.Hour, func(context.Context) error {
		ran = true
		return nil
	})
	if ran {
		t.Fatal("second step ran after the pass context ended, want it skipped")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("second step err = %v, want context.Canceled", err)
	}
}

// A removal needs its repository scanned first, so the worktree step must not
// stop at the per-step share. It still ends at the pass deadline.
func TestWorktreeCleanupIsNotCutAtTheStepShare(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("DYNA_ARCHIVE_DIR", "")
	var left time.Duration
	old := gcWorktrees
	gcWorktrees = func(ctx context.Context, _ string, _ worktrees.GCOptions) ([]worktrees.Tree, error) {
		deadline, _ := ctx.Deadline()
		left = time.Until(deadline)
		return nil, nil
	}
	t.Cleanup(func() { gcWorktrees = old })
	if _, err := Run(context.Background(), Options{Repos: []string{"repo"}}); err != nil {
		t.Fatal(err)
	}
	if left <= stepShare {
		t.Fatalf("worktree cleanup had %v left, want more than the %v step share", left, stepShare)
	}
}

// Each call opens the report database again, as a new hook process does, so
// the turn must come from storage rather than from process memory.
func TestWorktreeTurnSurvivesProcessRestart(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	for want := 0; want < 3; want++ {
		got, err := nextWorktreeTurn(context.Background(), store.DataDir())
		if err != nil || got != want {
			t.Fatalf("turn = %d, %v; want %d", got, err, want)
		}
	}
}

func TestFromTurnStartsAtNextRepositoryAndWraps(t *testing.T) {
	repos := []string{"a", "b", "c"}
	for turn, want := range [][]string{{"a", "b", "c"}, {"b", "c", "a"}, {"c", "a", "b"}, {"a", "b", "c"}} {
		if got := fromTurn(repos, turn); !reflect.DeepEqual(got, want) {
			t.Fatalf("turn %d order = %v, want %v", turn, got, want)
		}
	}
	if got := fromTurn(nil, 3); len(got) != 0 {
		t.Fatalf("no repositories gave %v, want none", got)
	}
}
