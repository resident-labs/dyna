package doctor

import (
	"errors"
	"fmt"
	"io"
	"time"
)

// DefaultCooldown is how long a finding that matches an earlier report of the
// same machine is not reported again.
const DefaultCooldown = 30 * time.Minute

// Pass is one run of the default doctor hook: it examines each machine whose
// samples the engine stores, under the owner's condition.
type Pass struct {
	DataDir  string
	Machines []string
	// GPUs reads live GPU memory during each examination; nil skips the GPU
	// check (D-352).
	GPUs func() ([]GPUReading, string)
	// If is the condition text, such as the hook recipe's DoctorCondition. It
	// holds every threshold, so the doctor has no limit of its own to start.
	If       string
	Cooldown time.Duration
	Now      time.Time
	// Post sends a new unhealthy report. A nil Post only saves it.
	Post func(Report) error
	// Alert runs on every pass that finds the machine unhealthy, even when the
	// report repeats an earlier one, so a caller can keep its own state per
	// audience. A nil Alert does nothing.
	Alert func(Report) error
	// Clear runs only when fresh samples confirm the machine is healthy. An
	// error or a missing sample never clears. A nil Clear does nothing.
	Clear func(machine string) error
}

// Do examines every machine and writes one line to log for each report that is
// new. A machine under its condition adds nothing to the log. A machine that
// fails to examine does not stop the others; the errors are joined.
func (p Pass) Do(log io.Writer) error {
	var errs []error
	for _, machine := range p.Machines {
		res, err := Run{DataDir: p.DataDir, Machine: machine, Target: "machine", Trigger: "hook schedule", If: p.If, Cooldown: p.Cooldown, Now: p.Now, Post: p.Post, GPUs: p.GPUs}.Do()
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", machine, err))
		}
		if err == nil && res.Ran && !res.Report.Healthy && p.Alert != nil {
			if aerr := p.Alert(res.Report); aerr != nil {
				errs = append(errs, fmt.Errorf("%s: %w", machine, aerr))
			}
		}
		if err == nil && res.Healthy && p.Clear != nil {
			if cerr := p.Clear(machine); cerr != nil {
				errs = append(errs, fmt.Errorf("%s: %w", machine, cerr))
			}
		}
		if res.Ran && !res.Repeat {
			if _, werr := fmt.Fprintln(log, res.Report.Text()); werr != nil {
				errs = append(errs, werr)
			}
		}
	}
	return errors.Join(errs...)
}
