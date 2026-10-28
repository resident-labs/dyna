package doctor

import (
	"errors"
	"fmt"
	"time"
)

// Run is one `dyna doctor examine` call.
type Run struct {
	DataDir, Machine, Target, Trigger, If string
	// GPUs reads live GPU memory during the examination; nil skips the GPU
	// check (D-352). Production callers pass ReadGPUs; tests stay hermetic.
	GPUs func() ([]GPUReading, string)
	// Cooldown skips a run whose finding matches a report of the same
	// target saved within it.
	Cooldown time.Duration
	Now      time.Time
	// Post sends an unhealthy report to the room. It runs after the save.
	Post func(Report) error
}

// Result is what a Run did. Ran is false when the condition did not hold;
// then nothing was examined or stored. Repeat means the cooldown matched an
// earlier report: Report has its ID but the numbers of this run.
type Result struct {
	Report Report
	Ran    bool
	Repeat bool
	// Healthy is true only when fresh samples confirm the machine is not under
	// the condition, or the examination found it healthy. An error, a missing
	// sample or a stale one leaves it false, because none of them proves health.
	Healthy bool
}

// Do checks the condition, then examines, saves and posts. A false
// condition reads only a few stored samples.
func (r Run) Do() (Result, error) {
	if r.Target != "machine" {
		return Result{}, fmt.Errorf("unknown target %q; this version examines machine", r.Target)
	}
	samples := Samples{DataDir: r.DataDir}
	samples.GPUs = r.GPUs
	met := ""
	if r.If != "" {
		cond, err := ParseCondition(r.If)
		if err != nil {
			return Result{}, err
		}
		read := func(group string, since time.Time) ([]Sample, error) { return samples.Window(r.Machine, group, since) }
		verdict, reading, err := cond.Evaluate(read, r.Now)
		if err != nil {
			return Result{}, err
		}
		if verdict != Met {
			// Only a verdict of Clear confirms health; Unknown leaves any alert as it is.
			return Result{Healthy: verdict == Clear}, nil
		}
		met = reading
	}
	rep, err := ExamineMachine(samples, r.Machine, r.Now)
	if err != nil {
		return Result{}, err
	}
	rep.Trigger, rep.Condition = r.Trigger, r.If
	if met != "" {
		rep.Summary += fmt.Sprintf(". Condition met: %s", met)
	}
	reports := Reports{DataDir: r.DataDir}
	if !rep.Healthy && r.Cooldown > 0 {
		prev, found, err := reports.Recent(rep.Machine, rep.Target, rep.Fingerprint, r.Now.Add(-r.Cooldown))
		if err != nil || found {
			if err != nil {
				return Result{}, err
			}
			// The cooldown keeps the earlier report's ID and delivery, but every
			// number is from this examination.
			rep.ID, rep.Delivered = prev.ID, prev.Delivered
			return r.deliver(reports, Result{Report: rep, Ran: true, Repeat: true})
		}
	}
	if rep.ID, err = reports.Save(rep); err != nil {
		return Result{}, err
	}
	return r.deliver(reports, Result{Report: rep, Ran: true, Healthy: rep.Healthy})
}

func (r Run) deliver(reports Reports, res Result) (Result, error) {
	rep := res.Report
	if !rep.Healthy && !rep.Delivered && r.Post != nil {
		if err := r.Post(rep); err != nil {
			return res, errors.Join(fmt.Errorf("report %d is saved but was not posted", rep.ID), err)
		}
		if err := reports.MarkDelivered(rep.ID); err != nil {
			return res, err
		}
		res.Report.Delivered = true
	}
	return res, nil
}
