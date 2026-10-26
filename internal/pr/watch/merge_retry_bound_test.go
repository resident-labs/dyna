package watch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A retry that builds the replacement batch in the command's own process must
// report an expiry instead of hanging without a word. The error names the step,
// the budget and where to look, and keeps context.DeadlineExceeded for callers.
func TestMergeRetryBuildIsBoundedAndSaysSo(t *testing.T) {
	past, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	err := runBounded(past, time.Minute, "merge retry", func(ctx context.Context) error {
		return ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired build = %v, want a deadline error", err)
	}
	if !strings.Contains(err.Error(), "merge retry did not finish within 1m0s") || !strings.Contains(err.Error(), "dyna git merge status") {
		t.Fatalf("error does not name the step, the budget and where to look: %v", err)
	}

	// A build that finishes keeps its own result.
	if err := runBounded(context.Background(), time.Minute, "merge retry", func(context.Context) error { return nil }); err != nil {
		t.Fatalf("finished build = %v, want nil", err)
	}
	sentinel := errors.New("build failed")
	if err := runBounded(context.Background(), time.Minute, "merge retry", func(context.Context) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("failed build = %v, want its own error", err)
	}
}
