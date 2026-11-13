package contract

import (
	"encoding/json"
	"time"
)

const (
	MethodWorkflowRun       Method = "workflow.run"
	MethodWorkflowReload    Method = "workflow.reload"
	MethodWorkflowStatus    Method = "workflow.status"
	MethodWorkflowStep      Method = "workflow.step"
	MethodWorkflowList      Method = "workflow.list"
	MethodWorkflowPin       Method = "workflow.pin"
	MethodWorkflowStepStart Method = "workflow.step_start"
	MethodWorkflowStepEnd   Method = "workflow.step_end"
	// MethodWorkflowStepTranscript names a running worker's session file, so its transcript opens before the worker ends.
	MethodWorkflowStepTranscript Method = "workflow.step_transcript"
	// MethodWorkflowSetStatus keeps a script's status() line on its run.
	MethodWorkflowSetStatus Method = "workflow.set_status"
	MethodWorkflowPause     Method = "workflow.pause"
	MethodWorkflowUnpause   Method = "workflow.unpause"
	MethodWorkflowSteer     Method = "workflow.steer"
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

// WorkflowReloadRequest gives a running run a new script, which its crew runs in place of the old one in the same box. Its
// meta keeps the run's name, repo and size; its profiles and description may change.
type WorkflowReloadRequest struct {
	Run    WorkflowRunID `json:"run"`
	Script string        `json:"script"`
}

// WorkflowReloadResponse names the script version the run now runs. The first script is version 1.
type WorkflowReloadResponse struct {
	Run     WorkflowRunID `json:"run"`
	Version int           `json:"version"`
}

// WorkflowInfo is one run. Steps counts its journal, Running and Failed count its running and failed steps, and FailedSteps
// names the failed, at most 20. Result is the script's return value, or why it threw; only status includes it, never list.
// List's Branch is the first recorded branch; status retains the most recent recorded branch. Version is the script version it runs, 1
// until a reload. Status is the script's latest status() line, empty when it wrote none.
type WorkflowInfo struct {
	Run          WorkflowRunID  `json:"run"`
	Room         RoomID         `json:"room"`
	Session      SessionID      `json:"session"`
	Subscription SubscriptionID `json:"subscription,omitempty"`
	Subject      string         `json:"subject,omitempty"`
	Name         string         `json:"name"`
	Description  string         `json:"description,omitempty"`
	Profiles     []ProfileName  `json:"profiles"`
	Repo         RepoName       `json:"repo,omitempty"`
	Commit       string         `json:"commit,omitempty"`
	Branch       string         `json:"branch,omitempty"` // the crew session's recorded branch, which its workers' branches extend
	Version      int            `json:"version,omitempty"`
	State        WorkflowState  `json:"state"`
	Paused       bool           `json:"paused,omitempty"`
	Result       string         `json:"result,omitempty"`
	Steps        int            `json:"steps,omitempty"`
	Failed       int            `json:"failed"`
	FailedSteps  []string       `json:"failedSteps,omitempty"`
	Running      int            `json:"running,omitempty"`
	Status       string         `json:"status,omitempty"`
	StatusAt     time.Time      `json:"statusAt,omitzero"`
	Usage        Usage          `json:"usage"`
	Started      time.Time      `json:"started"`
	Ended        time.Time      `json:"ended,omitzero"`
}

// WorkflowStatusLine is one status() line and when the script wrote it.
type WorkflowStatusLine struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// WorkflowStep is one journal row, without its result. Version is the script version that started it; a step a resumed run
// kept has none. Lane names the subject the step is about, as the script tagged it, and Phase its stage.
type WorkflowStep struct {
	Key        string            `json:"key"`
	Parent     string            `json:"parent,omitempty"`
	Kind       WorkflowStepKind  `json:"kind"`
	Name       string            `json:"name"`
	Phase      string            `json:"phase,omitempty"`
	Lane       string            `json:"lane,omitempty"`
	Profile    ProfileName       `json:"profile,omitempty"`
	Version    int               `json:"version,omitempty"`
	State      WorkflowStepState `json:"state"`
	Reason     string            `json:"reason,omitempty"`
	Transcript string            `json:"transcript,omitempty"` // the worker's session file, as transcript.append names it
	More       bool              `json:"more,omitempty"`       // workflow.step holds more than this row says
	Usage      Usage             `json:"usage"`
	Started    time.Time         `json:"started"`
	Ended      time.Time         `json:"ended,omitzero"`
}

// WorkflowStatusRequest names a run, or the crew session that hosts it.
type WorkflowStatusRequest struct {
	Run     WorkflowRunID `json:"run,omitempty"`
	Session SessionID     `json:"session,omitempty"`
}

// WorkflowStatusResponse carries the run's newest merge() result, when it has one, and its latest status() lines, oldest first.
type WorkflowStatusResponse struct {
	Run         WorkflowInfo         `json:"run"`
	Steps       []WorkflowStep       `json:"steps"`
	Merge       *WorkflowMerge       `json:"merge,omitempty"`
	StatusLines []WorkflowStatusLine `json:"statusLines,omitempty"`
}

// WorkflowStepRequest names one journal row by its crew session, which a closed crew keeps.
type WorkflowStepRequest struct {
	Session SessionID `json:"session"`
	Key     string    `json:"key"`
}

// WorkflowStepResponse is what a row leaves out: the prompt, command or text it ran, and its result as JSON text.
type WorkflowStepResponse struct {
	Detail string `json:"detail,omitempty"`
	Result string `json:"result,omitempty"`
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

// WorkflowListResponse contains lightweight summaries, without results or per-step detail.
type WorkflowListResponse struct {
	Runs []WorkflowInfo `json:"runs"`
}

// BoxWorkflow is what a crew's box runs. Each hello carries it, so a new box can restart the script, and a reload carries it
// to the box that runs the script.
type BoxWorkflow struct {
	Run      WorkflowRunID     `json:"run"`
	Name     string            `json:"name"`
	Script   string            `json:"script"`
	Version  int               `json:"version"`
	Paused   bool              `json:"paused"`
	Args     string            `json:"args,omitempty"`
	Profiles []WorkflowProfile `json:"profiles"` // in meta's order: the first is the default
	Workers  int               `json:"workers"`  // how many crew workers may run at once: the box's memory allows some, and the maxWorkers setting may allow fewer
	Timeout  int               `json:"timeout"`  // seconds an agent() worker may run when the script sets no timeout: the timeout setting
}

// WorkflowReload hands a crew's box a newer version of its script. The engine sends the run's current version on each channel,
// and a box already on it ignores it.
type WorkflowReload struct {
	Workflow BoxWorkflow `json:"workflow"`
}

func (WorkflowReload) BoxMessageType() BoxMessageType { return "workflow.reload" }
func (WorkflowReload) isBoxMessageBody()              {}

// WorkflowProfile is one profile of meta.profiles, as a worker needs it.
type WorkflowProfile struct {
	Name              ProfileName `json:"name"`
	Model             ModelChoice `json:"model"`
	CompactTokens     int         `json:"compactTokens,omitempty"`
	MaxConcurrent     int         `json:"maxConcurrent,omitempty"`
	ModelInstructions string      `json:"modelInstructions,omitempty"`
}

// WorkflowPinRequest reports the commit the crew's checkout started on. The first report stands.
type WorkflowPinRequest struct {
	Commit string `json:"commit"`
}
type WorkflowPinResponse struct{}

// WorkflowStepStartRequest looks a step up, and records its start when the journal has no result for it. Detail is the
// prompt, the command or the text. Version is the script version that made the call.
type WorkflowStepStartRequest struct {
	Key     string           `json:"key"`
	Parent  string           `json:"parent,omitempty"`
	Kind    WorkflowStepKind `json:"kind"`
	Name    string           `json:"name"`
	Phase   string           `json:"phase,omitempty"`
	Lane    string           `json:"lane,omitempty"`
	Profile ProfileName      `json:"profile,omitempty"`
	Detail  string           `json:"detail"`
	Version int              `json:"version,omitempty"`
	// Feature names the feature the call uses, so the settings can turn it off: worktree for agent() with a worktree, merge, run, workflow and post. Empty for a step that no switch gates.
	Feature WorkflowFeature `json:"feature,omitempty"`
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

// WorkflowSetStatusRequest is a script's status() line. It is not a step: the run keeps the latest line and a short history,
// and a resumed script writes its lines again as it runs.
type WorkflowSetStatusRequest struct {
	Text string `json:"text"`
}
type WorkflowSetStatusResponse struct{}

// WorkflowEnded closes a run. The final reply tells the caller; this is the room's record of it.
type WorkflowEnded struct {
	Run         WorkflowRunID `json:"run"`
	Session     SessionID     `json:"session"`
	Name        string        `json:"name"`
	State       WorkflowState `json:"state"`
	Steps       int           `json:"steps,omitempty"`
	Failed      int           `json:"failed"`
	FailedSteps []string      `json:"failedSteps,omitempty"`
	Usage       Usage         `json:"usage"`
}

func (WorkflowEnded) EventType() EventType { return "workflow.ended" }
func (WorkflowEnded) isEventBody()         {}

// WorkflowStepChanged carries a journal row to the run page, with the run's counts after it for the Runs list. It is live
// only, like TurnActivity: never stored, delivered to nobody, and the journal stays the record, so a page that missed one reads
// workflow.status again.
type WorkflowStepChanged struct {
	Run     WorkflowRunID `json:"run"`
	Session SessionID     `json:"session"`
	Step    WorkflowStep  `json:"step"`
	Steps   int           `json:"steps"`
	Running int           `json:"running"`
	Failed  int           `json:"failed"`
}

func (WorkflowStepChanged) EventType() EventType { return "workflow.step_changed" }
func (WorkflowStepChanged) isEventBody()         {}

// WorkflowStatusChanged carries a script's new status() line to the run page and the Runs list. It is live only, like
// WorkflowStepChanged, and the event's time is the line's.
type WorkflowStatusChanged struct {
	Run     WorkflowRunID `json:"run"`
	Session SessionID     `json:"session"`
	Status  string        `json:"status"`
}

func (WorkflowStatusChanged) EventType() EventType { return "workflow.status_changed" }
func (WorkflowStatusChanged) isEventBody()         {}

const MethodWorkflowState Method = "workflow.state"

type WorkflowStateRequest struct {
	Value json.RawMessage `json:"value,omitempty"`
}
type WorkflowStateResponse struct {
	Value json.RawMessage `json:"value"`
}

// WorkflowSteerRequest addresses one running worker by label or unique key prefix.
type WorkflowSteerRequest struct {
	Run  WorkflowRunID `json:"run"`
	Step string        `json:"step"`
	Text string        `json:"text"`
}
type WorkflowSteerResponse = PostResponse

type WorkflowPauseRequest struct {
	Run WorkflowRunID `json:"run"`
}
type WorkflowPauseResponse struct{}
type WorkflowUnpauseRequest = WorkflowPauseRequest
type WorkflowUnpauseResponse = WorkflowPauseResponse

// WorkflowPauseChanged refreshes open run pages after a pause or unpause.
type WorkflowPauseChanged struct {
	Run     WorkflowRunID `json:"run"`
	Session SessionID     `json:"session"`
	Paused  bool          `json:"paused"`
}

func (WorkflowPauseChanged) EventType() EventType { return "workflow.pause_changed" }
func (WorkflowPauseChanged) isEventBody()         {}
