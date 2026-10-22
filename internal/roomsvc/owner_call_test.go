package roomsvc

import (
	"context"

	"dyna-agent/internal/authority"
)

// ownerCall is what the engine passes a paired device's call: the owner.
var ownerCall = authority.WithActor(context.Background(), authority.Actor{Role: authority.Owner})
