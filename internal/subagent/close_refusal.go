package subagent

import "dyna-agent/internal/sessionclose"

type CloseReason = sessionclose.Reason

const (
	CloseReasonResponsibilities = sessionclose.ReasonResponsibilities
	CloseReasonGoalNeedsAgent   = sessionclose.ReasonGoalNeedsAgent
)

type CloseAction = sessionclose.Action

const (
	CloseActionRetry = sessionclose.ActionRetry
	CloseActionForce = sessionclose.ActionForce
)

type CloseRecovery = sessionclose.Recovery

// CloseRefusal keeps the existing human-readable error for CLI callers and
// carries its structured recovery for API callers.
type CloseRefusal struct {
	message  string
	cause    error
	recovery CloseRecovery
}

func (e *CloseRefusal) Error() string { return e.message }
func (e *CloseRefusal) Unwrap() error { return e.cause }

// CloseRecovery returns a copy of the recovery a client may read.
func (e *CloseRefusal) CloseRecovery() CloseRecovery {
	r := e.recovery
	r.Actions = append([]CloseAction{}, r.Actions...)
	r.Items = append([]Responsibility{}, r.Items...)
	return r
}

func newCloseRefusal(message string, cause error, reason CloseReason, actions []CloseAction, items []Responsibility) *CloseRefusal {
	return &CloseRefusal{message: message, cause: cause, recovery: CloseRecovery{
		Reason: reason, Actions: append([]CloseAction{}, actions...), Items: append([]Responsibility{}, items...),
	}}
}
