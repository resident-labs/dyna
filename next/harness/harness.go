package harness

import (
	"context"

	"github.com/resident-labs/dyna/contract"
)

// Harness drives a single retained session. Start returns when the turn settles.
// Steer and FollowUp may be called while Start runs.
type Harness interface {
	Start(context.Context, contract.TurnID, []contract.TurnInput) (contract.TurnEnded, error)
	Continue(context.Context, contract.TurnID) (contract.TurnEnded, error)
	Recover(contract.TurnID, []contract.TurnInput) (contract.TurnEnded, bool, error)
	Steer(context.Context, contract.TurnInput) error
	FollowUp(context.Context, contract.TurnInput) error
	Abort(context.Context) error
	// Delivered reports only IDs present in the persisted session.
	Delivered(context.Context, contract.DeliverySeq) (bool, error)
	Close() error
}
