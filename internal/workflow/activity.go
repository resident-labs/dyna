package workflow

import (
	"path/filepath"

	"dyna-agent/internal/activity"
	"dyna-agent/internal/runstore"
)

// observeActivity only handles step lifecycle events; it never waits for SQLite.
func (e *engine) observeActivity(ev runstore.Event) {
	if e.opts.Run == nil || ev.ID <= 0 {
		return
	}
	var kind activity.Kind
	var start, end bool
	switch ev.T {
	case "exec_start":
		kind, start = activity.CommandStep, true
	case "exec_run":
		kind = activity.CommandStep
	case "exec_end":
		kind, end = activity.CommandStep, true
	case "agent_start":
		kind, start = activity.AgentStep, true
	case "agent_end":
		kind, end = activity.AgentStep, true
	default:
		return
	}
	key := activity.Key{Kind: kind, Owner: e.runID(), Number: ev.ID}
	e.shared.activityMu.Lock()
	defer e.shared.activityMu.Unlock()
	if e.shared.activities == nil {
		e.shared.activities = make(map[activity.Key]*stepActivity)
	}
	a := e.shared.activities[key]
	if start {
		r := activity.Record{Signal: activity.Signal{Copier: activity.CopierRunning, CPU: activity.CPUUnsupported}, Lifecycle: activity.CopierRunning}
		if kind == activity.CommandStep {
			r.Reason = activity.ReasonStderrUnobserved
			r.Signal.Copier = activity.CopierUnknown
		} else {
			r.Session = runstore.AgentSessionID(key.Owner, key.Number)
		}
		p := activity.NewPublisher(filepath.Dir(filepath.Dir(e.opts.Run.Dir)), key, r)
		e.opts.Run.TrackActivity(p.Done())
		a = &stepActivity{publisher: p, record: r}
		e.shared.activities[key] = a
	}
	if a == nil {
		return
	}
	if ev.Shell != "" {
		a.record.Shell = ev.Shell
	}
	if end {
		a.record.Lifecycle = activity.CopierEnded
		if kind == activity.AgentStep {
			a.record.Signal.Copier = activity.CopierEnded
		}
	}
	a.publisher.Update(a.record)
	if end {
		a.publisher.Close()
		delete(e.shared.activities, key)
	}
}

// closeActivities marks any step without a terminal event unknown.
func (s *sharedState) closeActivities() {
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	for key, a := range s.activities {
		a.record.Lifecycle = activity.CopierUnknown
		a.record.Signal.Copier = activity.CopierUnknown
		a.record.Signal.CPU = activity.CPUUnknown
		a.record.Reason = activity.ReasonOutputUnobserved
		a.publisher.Update(a.record)
		a.publisher.Close()
		delete(s.activities, key)
	}
}
