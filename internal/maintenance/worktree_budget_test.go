package maintenance

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
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
