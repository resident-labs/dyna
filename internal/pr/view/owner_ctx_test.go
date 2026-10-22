package view_test

import (
	"context"

	"dyna-agent/internal/authority"
)

// ownerCtx is what the engine passes a paired device's call: the owner.
var ownerCtx = authority.WithActor(context.Background(), authority.Actor{Role: authority.Owner})
