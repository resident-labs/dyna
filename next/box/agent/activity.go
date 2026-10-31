package agent

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/resident-labs/dyna/box/shell"
	"github.com/resident-labs/dyna/contract"
)

type activity struct {
	mu     sync.Mutex
	latest contract.TurnActivityRequest
}

func (a *activity) set(turn contract.TurnID, label string) {
	label = shell.Mask(label)
	if r := []rune(label); len(r) > 80 {
		label = string(r[:79]) + "…"
	}
	a.mu.Lock()
	a.latest = contract.TurnActivityRequest{Turn: turn, Label: label}
	a.mu.Unlock()
}

func (a *activity) run(ctx context.Context, ticks <-chan time.Time, send func(context.Context, contract.TurnActivityRequest) error) {
	var sent contract.TurnActivityRequest
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			a.mu.Lock()
			latest := a.latest
			a.mu.Unlock()
			if latest.Turn != "" && latest != sent && send(ctx, latest) == nil {
				sent = latest
			}
		}
	}
}

func (c *Client) TurnActivity(ctx context.Context, req contract.TurnActivityRequest) error {
	body, _ := json.Marshal(req)
	_, err := c.post(ctx, contract.MethodTurnActivity, body)
	return err
}
