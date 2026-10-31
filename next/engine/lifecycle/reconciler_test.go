package lifecycle

import (
	"context"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestCancelWaitsForStep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	step := &activeStep{cancel: cancel, done: make(chan struct{})}
	r := &Reconciler{active: map[contract.BoxID]*activeStep{"box": step}}
	returned := make(chan struct{})
	go func() { r.Cancel("box"); close(returned) }()
	<-ctx.Done()
	select {
	case <-returned:
		t.Fatal("Cancel returned before the step finished")
	default:
	}
	close(step.done)
	<-returned
}
