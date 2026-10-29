package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"dyna-agent/internal/authority"
)

// ownerOrRoot is the check for controls that a root may also use, such as
// tearing down a stale local CI run. Other actors are refused, naming the call.
func ownerOrRoot(ctx context.Context, name string, _ []json.RawMessage) error {
	if role := authority.Of(ctx).Role; role != authority.Owner && role != authority.Root {
		return fmt.Errorf("%s is owner or root only", name)
	}
	return nil
}

// ownerOnly is the owner policy's check: the controls it guards — terminals,
// pairing, posting as the owner, and the like — have no CLI or agent path, so
// it refuses every actor but the owner, naming the control.
func ownerOnly(ctx context.Context, name string, _ []json.RawMessage) error {
	if authority.Of(ctx).Role != authority.Owner {
		return fmt.Errorf("%s is owner only", name)
	}
	return nil
}
