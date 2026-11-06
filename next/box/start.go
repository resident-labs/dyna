package box

import (
	"fmt"
	"github.com/resident-labs/dyna/contract"
	"time"
)

const (
	StartStepBudget        = 3 * time.Minute
	StartPreparationBudget = 15 * time.Minute
	StartCleanupBudget     = 30 * time.Second
	ProbeFailureLimit      = 3
)

// NamespaceSuperseded completes an old host operation without retry or failure:
// a newer creation/use owns its names. This is a host result, not engine state.
type NamespaceSuperseded struct{ Box contract.BoxID }

func (e NamespaceSuperseded) Error() string {
	return "box " + string(e.Box) + ": deletion superseded by a newer host epoch"
}

// StepTimeout is a host setup failure, unlike an outer shutdown, owner cancellation or repository deadline.
type StepTimeout struct {
	Host contract.HostName
	Step contract.StartStep
	Err  error
}

func (e StepTimeout) Unwrap() error { return e.Err }
func (e StepTimeout) Error() string {
	message := fmt.Sprintf("host %s: start step %s exceeded its idle/deadline budget", e.Host, e.Step)
	if e.Err != nil {
		return message + ": " + e.Err.Error()
	}
	return message
}
