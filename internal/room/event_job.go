package room

// JobState is how a job ended: the states the jobs table stores. The spelling
// is shared with internal/jobs, so a card never translates it.
type JobState string

// Job states, spelled as internal/jobs stores them.
const (
	JobRunning   JobState = "running"
	JobPassed    JobState = "passed"
	JobFailed    JobState = "failed"
	JobCancelled JobState = "cancelled"
	JobLost      JobState = "lost"
	JobTimedOut  JobState = "timed_out"
)

// jobStates lists every job state the web needs as a union type.
var jobStates = []JobState{JobRunning, JobPassed, JobFailed, JobCancelled, JobLost, JobTimedOut}

// JobEvent is the life of a detached job: the facts its card shows, so the
// card never reads the post text. The job ID stays opaque: it is the row's own
// key and the way to its log, never a label.
type JobEvent struct {
	// JobID is the jobs table's ID.
	JobID string `json:"jobId"`
	// Node is the machine name the job ran on.
	Node string `json:"node"`
	// Label is the job's command, as one short line.
	Label string   `json:"label"`
	State JobState `json:"state"`
	// ExitCode is the command's exit status, -1 when a stop ended the job
	// before it finished.
	ExitCode   int `json:"exitCode,omitempty"`
	DurationMS int `json:"durationMs,omitempty"`
	// Parent is the session that submitted the job, empty when the owner did.
	Parent string `json:"parent,omitempty"`
	// Detail is the rest of the line an agent reads: where the full log lives.
	Detail string `json:"detail,omitempty"`
}

func (JobEvent) eventFamily() EventFamily { return FamilyJob }

// eventType reads the state: a running job posts the start, any other state
// posts the result.
func (e JobEvent) eventType() EventType {
	if e.State == JobRunning {
		return TypeStarted
	}
	return TypeJobFinished
}

func (e JobEvent) eventSubject() string { return e.JobID }

// eventDedupe keys one post per job and type, so a start and its result are two
// posts that the web folds into one card.
func (e JobEvent) eventDedupe() string {
	return e.JobID + "\x00" + string(e.eventType())
}
