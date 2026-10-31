package agent

import (
	"context"
	"encoding/json"
	"time"

	"github.com/resident-labs/dyna/box/shell"
	"github.com/resident-labs/dyna/contract"
)

func (c *Client) watchShell(ctx context.Context, jobs *shell.Server, generation uint64, ticker *time.Ticker) {
	defer ticker.Stop()
	reset := true
	for {
		view := jobs.View(reset)
		if len(view) != 0 {
			body, _ := json.Marshal(contract.ShellOutputRequest{Generation: generation, Jobs: view})
			_, err := c.call(ctx, contract.MethodShellOutput, body)
			reset = err != nil
		} else {
			reset = false
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}
