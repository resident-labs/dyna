package runtime

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/images"
	"github.com/resident-labs/dyna/engine/lifecycle"
	"github.com/resident-labs/dyna/engine/store"
)

type occupiedImageHost struct {
	entered, release chan struct{}
	calls            atomic.Int32
}

func (h *occupiedImageHost) Build(context.Context, string, string, []byte) (bool, error) {
	if h.calls.Add(1) == 1 {
		close(h.entered)
	}
	<-h.release
	return false, nil
}
func TestImagePreparationDeadlineIncludesWaitingForAnotherBuild(t *testing.T) {
	host := &occupiedImageHost{entered: make(chan struct{}), release: make(chan struct{})}
	builder := &images.Builder{Now: time.Now}
	holderDone := make(chan struct{})
	go func() {
		defer close(holderDone)
		_, _ = builder.Build(context.Background(), host, "r", "h", "", "existing", "base", "tree", nil)
	}()
	<-host.entered
	var budget time.Duration
	b := &Boxes{stepContext: func(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
		budget = d
		return context.WithDeadline(ctx, time.Unix(0, 0))
	}}
	done := make(chan struct{})
	var err error
	go func() {
		defer close(done)
		err = b.startStep(context.Background(), lifecycle.BoxSpec{Box: "b", Host: "h"}, contract.StartImage, func(ctx context.Context) error {
			_, e := builder.Build(ctx, host, "r", "h", "", "waiter", "base", "tree", nil)
			return e
		})
	}()
	defer func() { close(host.release); <-holderDone; <-done }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("image deadline did not release a build-gate waiter")
	}
	var image box.ImageCreateError
	if budget != box.StartPreparationBudget || !errors.As(err, &image) || !errors.Is(err, context.DeadlineExceeded) || host.calls.Load() != 1 {
		t.Fatalf("gate expiry: budget %s, calls %d, error %v", budget, host.calls.Load(), err)
	}
}
func TestInstructionWaitAndSettleRespectCancellation(t *testing.T) {
	for _, operation := range []string{"sync", "settle"} {
		t.Run(operation, func(t *testing.T) {
			s, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			h, err := podman.New("owner@host", t.TempDir(), "h", nil)
			if err != nil {
				t.Fatal(err)
			}
			state := &instructionHost{gate: make(chan struct{}, 1)}
			state.gate <- struct{}{}
			b := &Boxes{Store: s, Layout: box.Layout{Data: t.TempDir()}, instructionSynced: map[*podman.Host]*instructionHost{h: state}}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			done := make(chan struct{})
			var result error
			go func() {
				defer close(done)
				if operation == "sync" {
					result = b.syncInstructions(ctx, h)
				} else {
					result = state.settle(ctx)
				}
			}()
			defer func() { <-state.gate; <-done }()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("canceled instruction gate waiter was not released")
			}
			if !errors.Is(result, context.Canceled) {
				t.Fatalf("canceled %s: %v", operation, result)
			}
		})
	}
}

type cleanupDeadlineRunner struct {
	work, joined time.Time
	joins        int
}

func (r *cleanupDeadlineRunner) Run(ctx context.Context, _ []byte, args ...string) ([]byte, error) {
	for _, a := range args {
		if a == "-M" || a == "-O" {
			return nil, nil
		}
	}
	deadline, _ := ctx.Deadline()
	if strings.Contains(args[len(args)-1], "dyna-cancel") {
		r.joined = deadline
		r.joins++
		return nil, nil
	}
	r.work = deadline
	return nil, errors.New("uncertain cleanup reply")
}
func TestCleanupReservesJoinTimeInsideOneTotalBudget(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	cancelParent()
	ctx, cancel := startCleanupContext(parent)
	defer cancel()
	if ctx.Err() != nil {
		t.Fatal("cleanup inherited canceled start")
	}
	runner := &cleanupDeadlineRunner{}
	h, err := podman.New("owner@host", t.TempDir(), "h", runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.RemoveContainer(ctx, "b"); err == nil {
		t.Fatal("uncertain reply became success")
	}
	if runner.joins != 1 || runner.work.IsZero() || runner.joined.Sub(runner.work) != box.StartCleanupBudget/2 {
		t.Fatalf("cleanup lost its total budget: work=%v join=%v joins=%d", runner.work, runner.joined, runner.joins)
	}
}
