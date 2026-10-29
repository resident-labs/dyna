package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"dyna-agent/internal/authority"
)

// ownerOnly is the owner policy's check: the controls it guards — terminals,
// pairing, posting as the owner, and the like — have no CLI or agent path, so
// it refuses every actor but the owner, naming the control.
func ownerOnly(ctx context.Context, name string, _ []json.RawMessage) error {
	if authority.Of(ctx).Role != authority.Owner {
		return fmt.Errorf("%s is owner only", name)
	}
	return nil
}
