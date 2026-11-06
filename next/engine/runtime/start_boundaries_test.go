package runtime

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/images"
	"github.com/resident-labs/dyna/engine/lifecycle"
)

// A bounded final stage must not add another cancellation decision after success:
// that would invoke failed-create cleanup where main publishes a successful box.
func TestCreateKeepsMainSuccessBoundaryAfterContainerStart(t *testing.T) {
	set := token.NewFileSet()
	file, err := parser.ParseFile(set, "boxes.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "Create" {
			continue
		}
		found := false
		var tail []string
		for _, statement := range function.Body.List {
			var source bytes.Buffer
			if err := format.Node(&source, set, statement); err != nil {
				t.Fatal(err)
			}
			if source.String() == `steps.Done("container start")` {
				found = true
				continue
			}
			if found {
				tail = append(tail, source.String())
			}
		}
		if got := strings.Join(tail, "\n"); !found || got != "ok = true\nreturn image.Tag, nil" {
			t.Fatalf("post-start success boundary must remain main's, got:\n%s", got)
		}
		return
	}
	t.Fatal("Boxes.Create not found")
}

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

func TestInstructionStartWaitRespectsCancellation(t *testing.T) {
	h, err := podman.New("owner@host", t.TempDir(), "h", nil)
	if err != nil {
		t.Fatal(err)
	}
	state := &instructionHost{gate: make(chan struct{}, 1)}
	state.gate <- struct{}{}
	defer func() { <-state.gate }()
	b := &Boxes{Layout: box.Layout{Data: t.TempDir()}, instructionSynced: map[*podman.Host]*instructionHost{h: state}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- b.syncInstructions(ctx, h) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("instruction wait: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled startup did not leave instruction gate")
	}
}
