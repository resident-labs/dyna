package agent

import (
	"context"
	"errors"
	"strings"
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
	a.set("turn_1", contract.ActivityTool, "first")
	a.set("turn_1", contract.ActivityTool, "second")
	secret := "sk-" + strings.Repeat("a", 40)
	a.set("turn_1", contract.ActivityTool, strings.Repeat("x", 60)+" "+secret)
	go a.run(ctx, ticks, func(_ context.Context, req contract.TurnActivityRequest) error {
		got <- req
		return errors.New("disconnected")
	})
	for range 2 {
		ticks <- time.Time{}
		if req := <-got; req.Turn != "turn_1" || req.Label != strings.Repeat("x", 60)+" [masked]" {
			t.Fatalf("got %+v", req)
		}
	}
}

func TestActivitySendsEveryFinishedCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time)
	got := make(chan contract.TurnActivityRequest, 4)
	a := &activity{}
	a.set("turn_1", contract.ActivityTool, "open")
	a.finish("turn_1", contract.ToolFinished{Call: "a", DurationMs: 8})
	a.finish("turn_1", contract.ToolFinished{Call: "b", DurationMs: 135})
	go a.run(ctx, ticks, func(_ context.Context, req contract.TurnActivityRequest) error {
		got <- req
		return nil
	})
	ticks <- time.Time{}
	for _, call := range []string{"a", "b"} {
		if req := <-got; req.Finished == nil || req.Finished.Call != call || req.Label != "" {
			t.Fatalf("got %+v, want a finish of %s", req, call)
		}
	}
	if req := <-got; req.Label != "open" || req.Finished != nil {
		t.Fatalf("got %+v, want the latest label", req)
	}
}
