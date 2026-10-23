package doctor

import (
	"errors"
	"fmt"
	"time"
)

// Run is one `dyna doctor examine` call.
type Run struct {
	DataDir, Machine, Target, Trigger, If string
	// Cooldown skips a run whose finding matches a report of the same
	// target saved within it.
	Cooldown time.Duration
	Now      time.Time
	// Post sends an unhealthy report to the room. It runs after the save.
	Post func(Report) error
}

// Result is what a Run did. Ran is false when the condition did not hold;
// then nothing was examined or stored. Repeat means Report is the earlier
// report that the cooldown matched.
type Result struct {
	Report Report
	Ran    bool
	Repeat bool
}

// Do checks the condition, then examines, saves and posts. A false
// condition reads only a few stored samples.
func (r Run) Do() (Result, error) {
	if r.Target != "machine" {
		return Result{}, fmt.Errorf("unknown target %q; this version examines machine", r.Target)
	}
	samples := Samples{DataDir: r.DataDir}
	met := ""
	if r.If != "" {
		cond, err := ParseCondition(r.If)
		if err != nil {
			return Result{}, err
		}
		read := func(group string, since time.Time) ([]Sample, error) { return samples.Window(r.Machine, group, since) }
		ok, reading, err := cond.Holds(read, r.Now)
		if err != nil || !ok {
			return Result{}, err
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
			return Result{Report: prev, Ran: true, Repeat: found}, err
		}
	}
	if rep.ID, err = reports.Save(rep); err != nil {
		return Result{}, err
	}
	res := Result{Report: rep, Ran: true}
	if !rep.Healthy && r.Post != nil {
		if err := r.Post(rep); err != nil {
			return res, errors.Join(fmt.Errorf("report %d is saved but was not posted", rep.ID), err)
		}
	}
	return res, nil
}
