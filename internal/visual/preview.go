package visual

import (
	"context"
	"time"

	"dyna-agent/internal/room"
)

// previewTime bounds the render a post waits for. A render that fails or
// runs longer leaves the card without a preview, and the post still goes.
const previewTime = 6 * time.Second

// Preview renders a card for its post. It runs in the posting process, never
// in the engine. On failure every check is skipped with the reason.
func Preview(ctx context.Context, doc []byte, allowNet bool) Result {
	ctx, cancel := context.WithTimeout(ctx, previewTime)
	defer cancel()
	r, err := Inspect(ctx, doc, room.CardCSP(allowNet, "'self'"), allowNet)
	if err == nil {
		return r
	}
	return Result{Checks: Skipped("preview unavailable: " + err.Error())}
}

// Skipped is every check, skipped for one reason.
func Skipped(reason string) []Check {
	var checks []Check
	for _, name := range []string{"renders", "console", "widths", "contrast", "network"} {
		checks = append(checks, Check{Name: name, Status: "skip", Detail: short(reason)})
	}
	return checks
}
