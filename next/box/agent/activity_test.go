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
	a.set("turn_1", "first")
	a.set("turn_1", "second")
	secret := "sk-" + strings.Repeat("a", 40)
	a.set("turn_1", strings.Repeat("x", 60)+" "+secret)
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
