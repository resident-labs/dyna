package workflowsvc

import (
	"time"

	"dyna-agent/internal/activity"
	"dyna-agent/internal/database"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/store"
)

// StepActivity joins a step's lifecycle to its output producer. A run()
// command's stderr bypasses the shell observer, so its output state stays
// unknown even when stdout has been observed.
func StepActivity(run string, number int, kind activity.Kind, now time.Time) (activity.Record, error) {
	db, err := database.Open(store.DataDir())
	if err != nil {
		return activity.Record{}, err
	}
	r, err := activity.Read(db, activity.Key{Kind: kind, Owner: run, Number: number}, now)
	if err != nil {
		return activity.Record{}, err
	}
	if r.Lifecycle == activity.CopierUnknown {
		return r, nil
	}
	switch kind {
	case activity.CommandStep:
		r.Signal.Copier = activity.CopierUnknown
		if r.Shell != "" {
			source, err := shells.DefaultStore().Activity(r.Shell, now)
			if err == nil {
				r.Signal.LastOutput = source.LastOutput
			}
		}
	case activity.AgentStep:
		if r.Session == "" {
			r.Reason = activity.ReasonOutputUnobserved
			r.Signal.CPU = activity.CPUUnknown
			r.Signal.Copier = activity.CopierUnknown
			return r, nil
		}
		source, err := activity.Read(db, activity.Key{Kind: activity.Turn, Owner: r.Session, Number: 1}, now)
		if err != nil {
			return activity.Record{}, err
		}
		r.Signal.LastOutput = source.Signal.LastOutput
		r.Reason = source.Reason
		r.Signal.CPU = source.Signal.CPU
		if source.Signal.Copier == activity.CopierUnknown {
			r.Signal.Copier = activity.CopierUnknown
			if r.Reason == activity.ReasonNone {
				r.Reason = activity.ReasonOutputUnobserved
			}
		}
	}
	return r, nil
}
