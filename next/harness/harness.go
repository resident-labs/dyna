package harness

import (
	"context"

	"github.com/resident-labs/dyna/contract"
)

// Harness drives a single retained session. Start returns when the turn settles.
// The started callback runs after Pi accepts the turn; Steer and FollowUp may then run.
type Harness interface {
	Start(context.Context, contract.TurnID, []contract.TurnInput, func()) (contract.TurnEnded, error)
	Continue(context.Context, contract.TurnID, func()) (contract.TurnEnded, error)
	// SetModel switches the model for later turns. It is never called during a turn.
	SetModel(context.Context, contract.ModelChoice) error
	Recover(contract.TurnID, []contract.TurnInput) (contract.TurnEnded, bool, error)
	Steer(context.Context, contract.TurnInput) error
	FollowUp(context.Context, contract.TurnInput) error
	Abort(context.Context) error
	// Delivered reports only IDs present in the persisted session.
	Delivered(context.Context, contract.DeliverySeq) (bool, error)
	Close() error
}
