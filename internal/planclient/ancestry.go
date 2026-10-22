// Package planclient sends workflow ancestry writes to the engine.
package planclient

import (
	"context"

	"dyna-agent/internal/engineclient"
)

// StampAncestry is the shared engine writer for workflow services and runners.
func StampAncestry(ctx context.Context, dir, parent string) (string, error) {
	var result string
	err := engineclient.New("").Call(ctx, "StampPlanAncestry", []any{dir, parent}, &result)
	return result, err
}
