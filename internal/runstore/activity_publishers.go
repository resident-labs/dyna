package runstore

import "dyna-agent/internal/activity"

// TrackActivity retains a step publisher until the run result is recorded.
func (r *Run) TrackActivity(done <-chan struct{}) {
	r.mu.Lock()
	r.activityDone = append(r.activityDone, done)
	r.mu.Unlock()
}

// FlushActivity gives final step observations a bounded chance to publish.
func (r *Run) FlushActivity() {
	r.mu.Lock()
	done := r.activityDone
	r.activityDone = nil
	r.mu.Unlock()
	activity.WaitFinal(done...)
}
