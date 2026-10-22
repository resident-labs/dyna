package computers

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWaitDefaultZeroAndCancellation(t *testing.T) {
	duration, err := waitDuration(parseActions(t, `[{"type":"wait"}]`)[0])
	if err != nil || duration != time.Second {
		t.Fatalf("default: %v %v", duration, err)
	}
	if err := executeAction(context.Background(), parseActions(t, `[{"type":"wait","ms":0}]`)[0], nil, nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := executeAction(ctx, parseActions(t, `[{"type":"wait","ms":10000}]`)[0], nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait ignored cancellation: %v", err)
	}
}
