package room

import "dyna-agent/internal/replayreason"

// RunState is how a workflow run ended.
type RunState string

// Run states.
const (
	RunCompleted RunState = "completed"
	RunFailed    RunState = "failed"
	RunStopped   RunState = "stopped"
	// RunInterrupted is a run that a Dyna update broke or stopped (D-402).
	RunInterrupted RunState = "interrupted"
)

// runStates lists every run state the web needs as a union type.
var runStates = []RunState{RunCompleted, RunFailed, RunStopped, RunInterrupted}

// RunEvent is the result of a workflow run: the facts its card
// shows, so the card never reads the post text.
type RunEvent struct {
	// RunID is the workflow's run ID: the card's key and the way to its view.
	RunID string `json:"runId"`
	// Name is the workflow's name, as its script wrote it.
	Name  string   `json:"name"`
	State RunState `json:"state"`
	// Summary is the run's result as one short line, or the failure cause.
	Summary string `json:"summary,omitempty"`
	// Steps is how many agent calls the run recorded, and Agents how many
	// worker sessions served them.
	Steps      int `json:"steps,omitempty"`
	Agents     int `json:"agents,omitempty"`
	DurationMS int `json:"durationMs,omitempty"`
	// Parent is the session that started the run, empty when an external
	// orchestrator or the owner did.
	Parent string `json:"parent,omitempty"`
	// ReplayReason is the automatic replay decision for an interrupted run.
	ReplayReason replayreason.WorkflowReplayReason `json:"replayReason,omitempty"`
	ReplayRun    string                            `json:"replayRun,omitempty"`
	// Detail is the rest of the line an agent reads: where the full output
	// lives, or the error the summary cuts short.
	Detail string `json:"detail,omitempty"`
}

func (RunEvent) eventFamily() EventFamily { return FamilyRun }
func (RunEvent) eventType() EventType     { return TypeResult }
func (r RunEvent) eventSubject() string   { return r.RunID }
func (r RunEvent) eventDedupe() string    { return r.RunID }
