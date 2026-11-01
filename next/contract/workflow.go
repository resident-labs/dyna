package contract

import "time"

const (
	MethodWorkflowRun       Method = "workflow.run"
	MethodWorkflowStatus    Method = "workflow.status"
	MethodWorkflowList      Method = "workflow.list"
	MethodWorkflowPin       Method = "workflow.pin"
	MethodWorkflowStepStart Method = "workflow.step_start"
	MethodWorkflowStepEnd   Method = "workflow.step_end"
	// MethodWorkflowStepTranscript names a running worker's session file, so its transcript opens before the worker ends.
	MethodWorkflowStepTranscript Method = "workflow.step_transcript"
)

// WorkerHeader names the worker of a crew's model call. A crew is one session with many conversations, and each needs its
// own account affinity. Only a crew's route reads it, so it can only divide that crew's own affinity.
const WorkerHeader = "X-Dyna-Worker"

// WorkflowState is where a run stands. A run that ended with failed steps is done: its script handled them.
type WorkflowState string

const (
	WorkflowRunning WorkflowState = "running"
	WorkflowDone    WorkflowState = "done"
	WorkflowFailed  WorkflowState = "failed" // the script threw
	WorkflowStopped WorkflowState = "stopped"
)

type WorkflowStepKind string

const (
	WorkflowStepAgent    WorkflowStepKind = "agent"
	WorkflowStepCommand  WorkflowStepKind = "run"
	WorkflowStepPost     WorkflowStepKind = "post"
	WorkflowStepWorkflow WorkflowStepKind = "workflow"
	WorkflowStepLog      WorkflowStepKind = "log"
)

type WorkflowStepState string

const (
	WorkflowStepRunning WorkflowStepState = "running"
	WorkflowStepDone    WorkflowStepState = "done"
	WorkflowStepFailed  WorkflowStepState = "failed"
)

// WorkflowRunRequest starts a crew. Args is JSON text. Resume names an ended run whose finished steps this one keeps.
type WorkflowRunRequest struct {
	Room     RoomID        `json:"room,omitempty"` // a session may omit it for its own
	Script   string        `json:"script"`
	Args     string        `json:"args,omitempty"`
	Resume   WorkflowRunID `json:"resume,omitempty"`
	ClientID string        `json:"clientId,omitempty"`
}
type WorkflowRunResponse struct {
	Run     WorkflowRunID `json:"run"`
	Session SessionID     `json:"session"`
	Box     BoxID         `json:"box"`
}

// WorkflowInfo is one run. Failed counts failed steps and FailedSteps names them, at most 20. Result is the script's return
// value, or why it threw.
type WorkflowInfo struct {
	Run         WorkflowRunID `json:"run"`
	Room        RoomID        `json:"room"`
	Session     SessionID     `json:"session"`
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Profiles    []ProfileName `json:"profiles"`
	Repo        RepoName      `json:"repo,omitempty"`
	Commit      string        `json:"commit,omitempty"`
	State       WorkflowState `json:"state"`
	Result      string        `json:"result,omitempty"`
	Failed      int           `json:"failed"`
	FailedSteps []string      `json:"failedSteps,omitempty"`
	Usage       Usage         `json:"usage"`
	Started     time.Time     `json:"started"`
	Ended       time.Time     `json:"ended,omitzero"`
}

// WorkflowStep is one journal row, without its result.
type WorkflowStep struct {
	Key        string            `json:"key"`
	Parent     string            `json:"parent,omitempty"`
	Kind       WorkflowStepKind  `json:"kind"`
	Name       string            `json:"name"`
	Phase      string            `json:"phase,omitempty"`
	Profile    ProfileName       `json:"profile,omitempty"`
	State      WorkflowStepState `json:"state"`
	Reason     string            `json:"reason,omitempty"`
	Transcript string            `json:"transcript,omitempty"` // the worker's session file, as transcript.append names it
	Usage      Usage             `json:"usage"`
	Started    time.Time         `json:"started"`
	Ended      time.Time         `json:"ended,omitzero"`
}

// WorkflowStatusRequest names a run, or the crew session that hosts it.
type WorkflowStatusRequest struct {
	Run     WorkflowRunID `json:"run,omitempty"`
	Session SessionID     `json:"session,omitempty"`
}

// WorkflowStatusResponse carries the run's newest merge() result, when it has one.
type WorkflowStatusResponse struct {
	Run   WorkflowInfo   `json:"run"`
	Steps []WorkflowStep `json:"steps"`
	Merge *WorkflowMerge `json:"merge,omitempty"`
}

// WorkflowMerge is what merge() returns and stores as its step's result.
type WorkflowMerge struct {
	Branch    string             `json:"branch"`
	Commit    string             `json:"commit"`
	Merged    []string           `json:"merged"`
	Conflicts []WorkflowConflict `json:"conflicts"`
}

// WorkflowConflict is a step that did not merge. A failed step has a reason and no files, because it left nothing to merge.
type WorkflowConflict struct {
	Step   string   `json:"step"`
	Branch string   `json:"branch,omitempty"`
	Files  []string `json:"files"`
	Reason string   `json:"reason,omitempty"`
}

// WorkflowListRequest names a room. A session may omit it for its own.
type WorkflowListRequest struct {
	Room RoomID `json:"room,omitempty"`
}
type WorkflowListResponse struct {
	Runs []WorkflowInfo `json:"runs"`
}

// BoxWorkflow is what a crew's box runs. Each hello carries it, so a new box can restart the script.
type BoxWorkflow struct {
	Run      WorkflowRunID     `json:"run"`
	Name     string            `json:"name"`
	Script   string            `json:"script"`
	Args     string            `json:"args,omitempty"`
	Profiles []WorkflowProfile `json:"profiles"` // in meta's order: the first is the default
	Workers  int               `json:"workers"`  // how many Pi processes the box's memory allows at once
}

// WorkflowProfile is one profile of meta.profiles, as a worker needs it.
type WorkflowProfile struct {
	Name          ProfileName `json:"name"`
	Model         ModelChoice `json:"model"`
	CompactTokens int         `json:"compactTokens,omitempty"`
	MaxConcurrent int         `json:"maxConcurrent,omitempty"`
}

// WorkflowPinRequest reports the commit the crew's checkout started on. The first report stands.
type WorkflowPinRequest struct {
	Commit string `json:"commit"`
}
type WorkflowPinResponse struct{}

// WorkflowStepStartRequest looks a step up, and records its start when the journal has no result for it. Detail is the
// prompt, the command or the text.
type WorkflowStepStartRequest struct {
	Key     string           `json:"key"`
	Parent  string           `json:"parent,omitempty"`
	Kind    WorkflowStepKind `json:"kind"`
	Name    string           `json:"name"`
	Phase   string           `json:"phase,omitempty"`
	Profile ProfileName      `json:"profile,omitempty"`
	Detail  string           `json:"detail"`
}

// WorkflowStepStartResponse carries the stored outcome. An empty State means the step has none and is now running.
type WorkflowStepStartResponse struct {
	State  WorkflowStepState `json:"state,omitempty"`
	Result string            `json:"result,omitempty"`
	Reason string            `json:"reason,omitempty"`
}

// WorkflowStepEndRequest stores a step's outcome. Result is JSON text.
type WorkflowStepEndRequest struct {
	Key        string            `json:"key"`
	State      WorkflowStepState `json:"state"`
	Result     string            `json:"result,omitempty"`
	Reason     string            `json:"reason,omitempty"`
	Transcript string            `json:"transcript,omitempty"`
	Usage      Usage             `json:"usage"`
}
type WorkflowStepEndResponse struct{}

// WorkflowStepTranscriptRequest names a running step's worker file, as transcript.append names it.
type WorkflowStepTranscriptRequest struct {
	Key        string `json:"key"`
	Transcript string `json:"transcript"`
}
type WorkflowStepTranscriptResponse struct{}

// WorkflowEnded closes a run. The final reply tells the caller; this is the room's record of it.
type WorkflowEnded struct {
	Run         WorkflowRunID `json:"run"`
	Session     SessionID     `json:"session"`
	Name        string        `json:"name"`
	State       WorkflowState `json:"state"`
	Failed      int           `json:"failed"`
	FailedSteps []string      `json:"failedSteps,omitempty"`
	Usage       Usage         `json:"usage"`
}

func (WorkflowEnded) EventType() EventType { return "workflow.ended" }
func (WorkflowEnded) isEventBody()         {}

// WorkflowStepChanged carries a journal row to the run page. It is live only, like TurnActivity: never stored, delivered to
// nobody, and the journal stays the record, so a page that missed one reads workflow.status again.
type WorkflowStepChanged struct {
	Run     WorkflowRunID `json:"run"`
	Session SessionID     `json:"session"`
	Step    WorkflowStep  `json:"step"`
}

func (WorkflowStepChanged) EventType() EventType { return "workflow.step_changed" }
func (WorkflowStepChanged) isEventBody()         {}
