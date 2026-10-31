package agent

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestActivityCoalesces(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time)
	got := make(chan contract.TurnActivityRequest)
	a := &activity{}
	a.set("turn_1", "first")
	a.set("turn_1", "second")
	a.set("turn_1", "last")
	go a.run(ctx, ticks, func(_ context.Context, req contract.TurnActivityRequest) { got <- req })
	ticks <- time.Time{}
	if req := <-got; req.Turn != "turn_1" || req.Label != "last" {
		t.Fatalf("got %+v", req)
	}
}
