package localci

import (
	"testing"

	"dyna-agent/internal/jobs"
	"dyna-agent/internal/jobstest"
)

// fakeMachine is the shared jobstest fake node, wired to a local CI runner.
type fakeMachine struct {
	*jobstest.Fake
}

func newFakeMachine(t *testing.T, legacy jobs.Command) *fakeMachine {
	proto := jobstest.Protocol{Start: jobs.StartScript, Poll: jobs.PollScript, Stop: jobs.StopScript}
	return &fakeMachine{jobstest.NewFake(t, proto, jobstest.RunFunc(legacy))}
}

// attach makes the runner talk to the fake, on a clock that only the
// executor's own waits advance.
func (m *fakeMachine) attach(r *Runner) {
	r.Command = jobs.Command(m.CommandFunc())
	r.wait = m.WaitFunc()
	r.clock = m.ClockFunc()
}
