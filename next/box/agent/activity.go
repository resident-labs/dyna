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
	done   []contract.TurnActivityRequest // finished calls not yet sent, each of its own
}

func (a *activity) set(turn contract.TurnID, kind contract.ActivityKind, label string) {
	label = shell.Mask(label)
	if r := []rune(label); len(r) > 80 {
		label = string(r[:79]) + "…"
	}
	a.mu.Lock()
	a.latest = contract.TurnActivityRequest{Turn: turn, Kind: kind, Label: label}
	a.mu.Unlock()
}

// finish queues one call of a parallel batch that ended. Unlike the latest label, each one is sent.
func (a *activity) finish(turn contract.TurnID, finished contract.ToolFinished) {
	a.mu.Lock()
	a.done = append(a.done, contract.TurnActivityRequest{Turn: turn, Finished: &finished})
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
			latest, done := a.latest, a.done
			a.done = nil
			a.mu.Unlock()
			for _, req := range done {
				_ = send(ctx, req) // a live signal: the result still comes with the transcript
			}
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
