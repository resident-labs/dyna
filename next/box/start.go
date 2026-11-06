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
