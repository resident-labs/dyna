package subagent

import (
	"context"
	"dyna-agent/internal/computers"
	"time"
)

// All durable session-close paths release their computer. Cleanup is bounded
// independently of the turn context, which is often canceled during finish.
func destroyComputer(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := computers.Destroy(ctx, id); err != nil {
		computers.RecordFailure(id, err)
	}
}
