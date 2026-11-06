package runtime

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
)

// A complete successful frame already exists when cancellation reaches the adapter.
// The wrapper must not turn that success into a new failed-create cleanup decision.
type completedStartRunner struct {
	base      *stackRunner
	cancel    context.CancelFunc
	completed bool
}

func (r *completedStartRunner) Run(ctx context.Context, in []byte, args ...string) ([]byte, error) {
	out, err := r.base.Run(ctx, in, args...)
	if err == nil && len(args) > 0 && strings.Contains(args[len(args)-1], "'start' 'box_1'") && bytes.HasSuffix(out, []byte(" 000\n")) {
		r.completed = true
		r.cancel()
	}
	return out, err
}

func TestCreateCompletedStartKeepsMainCleanupBoundary(t *testing.T) {
	base := &stackRunner{}
	b, spec := creatingBoxes(t, base)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &completedStartRunner{base: base, cancel: cancel}
	host, err := podman.New("user@host", t.TempDir(), "host", runner, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b.hosts[spec.Host] = host
	image, err := b.Create(ctx, spec)
	if !runner.completed || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("fixture did not cancel while the successful final frame unwound")
	}
	if err != nil || image == "" {
		t.Errorf("completed StartContainer success became a failed create: image=%q error=%v", image, err)
	}
	for _, command := range base.commands {
		if strings.Contains(command, "'rm' '--ignore' '--volumes' 'box_1'") || strings.Contains(command, "'pod' 'rm'") {
			t.Errorf("completed container start acquired an extra create-error cleanup trigger: %s", command)
		}
	}
}

func TestCompletedStepSuccessSurvivesCancellationAndDeadline(t *testing.T) {
	for _, step := range []contract.StartStep{contract.StartInstructions, contract.StartBuildCache, contract.StartShared, contract.StartImage, contract.StartSlice, contract.StartForward, contract.StartStack, contract.StartCreate, contract.StartSidecars, contract.StartContainer} {
		for _, signal := range []string{"parent cancellation", "step deadline"} {
			t.Run(string(step)+"/"+signal, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					b := &Boxes{}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					err := b.startStep(ctx, lifecycle.BoxSpec{Box: "b", Host: "host"}, step, func(child context.Context) error {
						if signal == "parent cancellation" {
							cancel()
						} else {
							<-child.Done()
							if !errors.Is(child.Err(), context.DeadlineExceeded) {
								t.Fatalf("step did not reach its deadline: %v", child.Err())
							}
						}
						return nil // The operation reports a confirmed result, not an incomplete wait.
					})
					if err != nil {
						t.Fatalf("confirmed %s success replaced by %s: %v", step, signal, err)
					}
				})
			})
		}
	}
}

func TestStepFailureRetainsItsErrorAndOuterCancellation(t *testing.T) {
	failure := errors.New("confirmed operation failure")
	b := &Boxes{}
	spec := lifecycle.BoxSpec{Box: "b", Host: "host"}
	if err := b.startStep(context.Background(), spec, contract.StartContainer, func(context.Context) error { return failure }); err != failure {
		t.Fatalf("operation error changed without a context signal: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := b.startStep(ctx, spec, contract.StartContainer, func(context.Context) error {
		cancel()
		return failure
	})
	if !errors.Is(err, failure) || !errors.Is(err, context.Canceled) {
		t.Fatalf("failed result lost its operation error or outer cancellation: %v", err)
	}
}
