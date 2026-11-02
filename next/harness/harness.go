package harness

import (
	"context"
	"errors"

	"github.com/resident-labs/dyna/contract"
)

// ErrRefused marks a command the agent program answered with a refusal, as opposed to a broken connection to it.
var ErrRefused = errors.New("refused by the agent program")

// ModelTransfer reports how TransferModel ended. Path is Pi's: unchanged, switched, compacted, boundary or aborted.
type ModelTransfer struct {
	Path         string
	Reason       string // why a compaction the transfer wanted did not happen or was not enough
	TokensBefore int64
	TokensAfter  int64
}

// TransferAborted is the path of a transfer that a stop cancelled. The model did not change.
const TransferAborted = "aborted"

// Compaction reports how Compact ended. Compacted is false when the context did not need it, so nothing ran.
type Compaction struct {
	Compacted    bool
	TokensBefore int64
	TokensAfter  int64
	Usage        contract.Usage // Model is the caller's to fill in
}

// Harness drives a single retained session. Start returns when the turn settles.
// The started callback runs after Pi accepts the turn; Steer and FollowUp may then run. Abort may run sooner, during TransferModel.
type Harness interface {
	Start(context.Context, contract.TurnID, []contract.TurnInput, func()) (contract.TurnEnded, error)
	Continue(context.Context, contract.TurnID, func()) (contract.TurnEnded, error)
	// TransferModel moves the session to a model, thinking level and compaction trigger (0: Pi's default) before a
	// turn's prompt. It may summarize the conversation, so it can take minutes; Abort cancels it. ErrRefused means the
	// agent program refused the model. A failed summary is no error: Pi reports it in the transfer and still switches.
	TransferModel(context.Context, contract.TurnID, contract.ModelChoice, int) (ModelTransfer, error)
	// Compact summarizes the conversation between turns if its context is over the given size, as the agent program measures
	// it. It can take minutes; Abort cancels it. A turn must wait for it to return.
	Compact(context.Context, int64) (Compaction, error)
	// SetPromptSections applies the dispatched context before starting or continuing a turn.
	SetPromptSections(context.Context, []contract.ContextSection) error
	Recover(contract.TurnID, []contract.TurnInput) (contract.TurnEnded, bool, error)
	Steer(context.Context, contract.TurnInput) error
	FollowUp(context.Context, contract.TurnInput) error
	Abort(context.Context) error
	// Delivered reports only IDs present in the persisted session.
	Delivered(context.Context, contract.DeliverySeq) (bool, error)
	Close() error
}
