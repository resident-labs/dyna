package contract

import "time"

// StreamHello is sent before events on each stream connection.
type StreamHello struct {
	Build       string `json:"build"`
	HeartbeatMs int    `json:"heartbeatMs,omitempty"` // named heartbeat cadence; absent on older servers
	RoomUnion   bool   `json:"roomUnion,omitempty"`   // owner streams support independent attention and room cursors
}

// EventType is "family.verb" in the past tense, such as "post.created".
type EventType string

type Event struct {
	Seq  int64     `json:"seq"` // total order in the store, set at commit
	ID   EventID   `json:"id"`
	Time time.Time `json:"at"`
	Room RoomID    `json:"room,omitempty"` // empty for machine events
	From Principal `json:"from"`
	Body EventBody `json:"body"` // the JSON form also carries "type"
}

// EventBody is one registered body struct. The unexported method seals the
// set, so every body is defined in contract/.
type EventBody interface {
	EventType() EventType
	isEventBody()
}

type RoomLimitsChanged struct {
	CPUs     int   `json:"cpus"`
	MemBytes int64 `json:"memBytes"`
}

func (RoomLimitsChanged) EventType() EventType { return "room.limits_changed" }
func (RoomLimitsChanged) isEventBody()         {}

// HostSwitchesChanged audits an owner's host.set in each room, without waking sessions.
// Only settings whose stored values changed are present; From names the owner.
type HostSwitchesChanged struct {
	Host    HostName `json:"host"`
	Agents  *bool    `json:"agents,omitempty"`
	CI      *bool    `json:"ci,omitempty"`
	CIScale *int     `json:"ciScale,omitempty"`
}

func (HostSwitchesChanged) EventType() EventType { return "host.switches_changed" }
func (HostSwitchesChanged) isEventBody()         {}

type RoomPaused struct{}
type RoomResumed struct{}

func (RoomPaused) EventType() EventType  { return "room.paused" }
func (RoomPaused) isEventBody()          {}
func (RoomResumed) EventType() EventType { return "room.resumed" }
func (RoomResumed) isEventBody()         {}

// RoomDeleting records the owner's delete. The room refuses new work from here on.
type RoomDeleting struct{}

// RoomDeleted is a live event with sequence 0: the room's rows are gone, so no row can hold it.
type RoomDeleted struct{}

func (RoomDeleting) EventType() EventType { return "room.deleting" }
func (RoomDeleting) isEventBody()         {}
func (RoomDeleted) EventType() EventType  { return "room.deleted" }
func (RoomDeleted) isEventBody()          {}

// SessionProfileChanged tells a session that its profile, and so its model, changed.
type SessionProfileChanged struct {
	Session SessionID   `json:"session"`
	Profile ProfileName `json:"profile"`
}

func (SessionProfileChanged) EventType() EventType { return "session.profile_changed" }
func (SessionProfileChanged) isEventBody()         {}

// PostSource identifies a cross-room post's author after its room is removed.
type PostSource struct {
	Room        RoomID   `json:"room"`
	RoomName    RoomName `json:"roomName"`
	SessionName string   `json:"sessionName"`
}

// PostSent records an outgoing cross-room post in its sender's feed.
type PostSent struct {
	Text   string     `json:"text"`
	Target PostSource `json:"target"`
}

// PostCreated is a post in a room.
type PostCreated struct {
	Spoken         bool         `json:"spoken,omitempty"`
	DataAttachment *int         `json:"dataAttachment,omitempty"` // index of the associated JSON file; other attachments remain visible
	Source         *PostSource  `json:"source,omitempty"`         // immutable identity for a cross-room post
	Text           string       `json:"text"`
	Data           JSONData     `json:"data,omitempty"`
	Turn           TurnID       `json:"turn,omitempty"`
	Mentions       []Principal  `json:"mentions,omitempty"`
	WorkflowStep   string       `json:"workflowStep,omitempty"`
	Home           Conversation `json:"home"` // feed or a DM
	ReplyTo        EventID      `json:"replyTo,omitempty"`
	Attachments    []Attachment `json:"attachments,omitempty"`
	ClientID       string       `json:"clientId,omitempty"` // the sender's PostRequest.ClientID, so a pending row is replaced exactly
	ToolCallID     string       `json:"toolCallId,omitempty"`
	// Hook names the subscription whose Run wrote this post. The engine sends it, but the text is the script's.
	Hook SubscriptionID `json:"hook,omitempty"`
}

type Attachment struct {
	Name   string      `json:"name"`
	Media  string      `json:"media"` // MIME type
	Bytes  int64       `json:"bytes"`
	SHA256 string      `json:"sha256"` // the file is stored by hash
	Visual *VisualCard `json:"visual,omitempty"`
}

// VisualCard describes a checked HTML attachment. The engine assigns ID and Version.
type VisualCard struct {
	ID        EventID       `json:"id"`
	Version   int           `json:"version"`
	Title     string        `json:"title"`
	Height    int           `json:"height"`
	AllowNet  bool          `json:"allowNet"`
	Banner    string        `json:"banner"`
	Thumbnail string        `json:"thumbnail"`
	Shots     []VisualShot  `json:"shots"`
	Checks    []VisualCheck `json:"checks"`
}

type VisualShot struct {
	Width int    `json:"width"`
	File  string `json:"file"`
}

type VisualCheckName string

const (
	VisualCheckRenders  VisualCheckName = "renders"
	VisualCheckConsole  VisualCheckName = "console"
	VisualCheckWidths   VisualCheckName = "widths"
	VisualCheckContrast VisualCheckName = "contrast"
	VisualCheckNetwork  VisualCheckName = "network"
)

type VisualCheckStatus string

const (
	VisualCheckPass VisualCheckStatus = "pass"
	VisualCheckWarn VisualCheckStatus = "warn"
	VisualCheckFail VisualCheckStatus = "fail"
	VisualCheckSkip VisualCheckStatus = "skip"
)

type VisualCheck struct {
	Name   VisualCheckName   `json:"name"`
	Status VisualCheckStatus `json:"status"`
	Detail string            `json:"detail,omitempty"`
}

// SessionStarted is a session starting, with its goal and brief. Workflow marks a crew, whose page is its run.
type SessionStarted struct {
	Session  SessionID   `json:"session"`
	Name     string      `json:"name"`
	Parent   SessionID   `json:"parent,omitempty"` // empty for root
	Profile  ProfileName `json:"profile"`
	Goal     string      `json:"goal,omitempty"`
	DoneWhen string      `json:"doneWhen,omitempty"`
	Brief    string      `json:"brief,omitempty"`
	Workflow bool        `json:"workflow,omitempty"`
	Advisor  bool        `json:"advisor,omitempty"` // the room's advisor, which sits under root apart from its children
}

// SessionRestarted is a session's box being replaced, updated or started again. With neither flag the box was recreated from
// nothing after it failed.
type SessionRestarted struct {
	Session SessionID `json:"session"`
	// ImageUpdated marks a new container on the same volume, as opposed to a box restarted from nothing.
	ImageUpdated bool `json:"imageUpdated,omitempty"`
	// Stopped marks the same container started again after it stopped, as after a host reboot or a crash.
	Stopped bool `json:"stopped,omitempty"`
	// Refreshed marks a box the session asked to restart on the current binary. That notice always wakes.
	Refreshed bool `json:"refreshed,omitempty"`
	// OpenGoal is whether the session held an open goal when the engine wrote the notice. The notice then tells it to continue,
	// and wakes it.
	OpenGoal bool `json:"openGoal,omitempty"`
	// ClientID names one box start, so the same restart told twice reaches the session once.
	ClientID string `json:"clientId,omitempty"`
}

type SessionMoved struct {
	Session   SessionID `json:"session"`
	NewParent SessionID `json:"newParent"`
}

type SessionCloseReason string

const (
	SessionCloseDormant SessionCloseReason = "dormant"
	SessionCloseDisk    SessionCloseReason = "disk"
)

// SessionClosed is a session closing, with what it handed on.
type SessionClosed struct {
	Reason  SessionCloseReason `json:"reason,omitempty"`
	Session SessionID          `json:"session"`
	Unheld  []GoalID           `json:"unheld,omitempty"` // open goals left with no holder, because the parent is root (7.4)
	// Handed is the owed work that moved to the parent.
	Handed []Responsibility `json:"handed,omitempty"`
	// Folded is the goal whose work became an item of an ancestor's goal, since a session holds one goal.
	Folded []GoalFold `json:"folded,omitempty"`
}

// GoalFold is a closed session's goal that became item Item of goal Into, with its open items as that item's subtasks.
type GoalFold struct {
	Goal GoalID `json:"goal"`
	Into GoalID `json:"into"`
	Item int    `json:"item"`
}

// SessionCompacted records one compaction of a quiet box before its stop, or a failed try. Pi writes the compaction itself
// to the transcript. This records its usage against the session, and the turn, so a quiet period compacts once.
type SessionCompacted struct {
	Session      SessionID `json:"session"`
	Turn         TurnID    `json:"turn"` // the session's last turn when the box was asked
	TokensBefore int64     `json:"tokensBefore,omitempty"`
	TokensAfter  int64     `json:"tokensAfter,omitempty"`
	Usage        Usage     `json:"usage"`
	Error        string    `json:"error,omitempty"`
}

type TurnStarted struct {
	Session SessionID `json:"session"`
	Turn    TurnID    `json:"turn"`
	Inputs  []EventID `json:"inputs"` // the deliveries this turn takes
}

type TurnOutcome string // "completed" | "failed" | "stopped" | "closed"

// TurnEnded is a session's turn ending.
type TurnEnded struct {
	DataAttachment *int         `json:"dataAttachment,omitempty"`
	Data           JSONData     `json:"data,omitempty"`
	Attachments    []Attachment `json:"attachments,omitempty"`
	Session        SessionID    `json:"session"`
	Turn           TurnID       `json:"turn"`
	Outcome        TurnOutcome  `json:"outcome"`
	Final          string       `json:"final,omitempty"`         // the final response
	WaitingNotice  string       `json:"waitingNotice,omitempty"` // replaces Final for session recipients while waiting
	Replies        []FinalReply `json:"replies,omitempty"`       // where Final was delivered
	Usage          Usage        `json:"usage"`
	Error          string       `json:"error,omitempty"` // failed only
	Cause          FailureCause `json:"cause,omitempty"` // failed only
	// ShellJobs counts the background shell jobs still running when the turn ended. Each job's
	// end wakes the session, so the next turn.ended carries the new count.
	ShellJobs int `json:"shellJobs,omitempty"`
	// Jobs names the jobs ShellJobs counts. A box agent too old to name them sends only the count.
	Jobs []ShellJobID `json:"jobs,omitempty"`
	// EndPosts carries the harness's result to TurnOutputRequest.EndPosts. The event records the same fact in Replies.
	EndPosts []EventID `json:"-"`
	// Unread carries the runner's result to TurnOutputRequest.Unread.
	Unread []DeliverySeq `json:"-"`
}

type ConversationRead struct {
	Room         RoomID       `json:"room"`
	Conversation Conversation `json:"conversation"`
	Seq          int64        `json:"seq"`
}

// TurnActivity is live only. The engine keeps each running turn's latest label and its kind in memory and
// never stores this event, so it has seq 0, events.list never returns it, and it does not move a
// stream's resume point. turn.ended clears it.
type ActivityKind string // "tool" | "compaction" | "model" | "model_wait"

const (
	ActivityTool       ActivityKind = "tool"       // a tool call; the label names it
	ActivityCompaction ActivityKind = "compaction" // the conversation is compacting
	ActivityModel      ActivityKind = "model"      // the model is changing
	ActivityModelWait  ActivityKind = "model_wait" // waiting for a model response
)

type TurnActivity struct {
	Session SessionID    `json:"session"`
	Turn    TurnID       `json:"turn"`
	Kind    ActivityKind `json:"kind"`  // what the turn is doing; the label only words it
	Label   string       `json:"label"` // the running tool call, masked, one line of at most 80 characters
}

// ToolFailed records one tool call a session's transcript shows failing. Error and args are cut short and can hold outside text.
type ToolFailed struct {
	Session SessionID `json:"session"`
	Tool    string    `json:"tool"`
	Model   string    `json:"model,omitempty"`
	Args    string    `json:"args,omitempty"` // the call's arguments as JSON, when the call was seen
	Error   string    `json:"error"`
}

// TranscriptChanged is an owner-only live notification; it contains no transcript text.
type TranscriptChanged struct {
	Session SessionID `json:"session"`
	Turn    TurnID    `json:"turn"`
}

type FinalReply struct {
	Reply  ReplyChannel `json:"reply"`
	Posted EventID      `json:"posted,omitempty"`
}

type Usage struct {
	Model            string `json:"model"`
	InputTokens      int64  `json:"inputTokens"`
	OutputTokens     int64  `json:"outputTokens"`
	CacheReadTokens  int64  `json:"cacheReadTokens"`
	CacheWriteTokens int64  `json:"cacheWriteTokens"`
}

// ShellJobEnded reports a background shell job that ended. It wakes the session once.
type ShellJobEnded struct {
	Description string      `json:"description,omitempty"`
	End         ShellJobEnd `json:"end"`
	Session     SessionID   `json:"session"`
	Job         ShellJobID  `json:"job"`
	Tail        string      `json:"tail"`
}

// ShellJobRunning reports a background shell job still running. It wakes a session with no open turn. One with an open turn
// reads it with its next turn.
type ShellJobRunning struct {
	Description  string     `json:"description,omitempty"`
	Session      SessionID  `json:"session"`
	Job          ShellJobID `json:"job"`
	RanMinutes   int        `json:"ranMinutes"`
	QuietMinutes *int       `json:"quietMinutes,omitempty"`
	Tail         string     `json:"tail"`
	IdleMinutes  *int       `json:"idleMinutes,omitempty"` // set once the job has had no output, CPU use, reads or writes for this long
	Processes    string     `json:"processes,omitempty"`   // its processes, busiest first, one a line, masked; empty for a job on a host
}

type ComputerState string

const (
	ComputerRunning ComputerState = "running"
	ComputerStopped ComputerState = "stopped"
)

type ComputerChanged struct {
	Session SessionID     `json:"session"`
	To      ComputerState `json:"to"`
}

// BoxChanged is a workspace changing state or reporting a lifecycle failure, with its session, host, cause and reason.
type BoxChanged struct {
	Box     BoxID        `json:"box"`
	Session SessionID    `json:"session"`
	Host    HostName     `json:"host,omitempty"`
	From    BoxState     `json:"from,omitempty"` // empty for a new box
	To      BoxState     `json:"to"`
	Cause   FailureCause `json:"cause,omitempty"` // a failed box, or a close that could not archive
	Phase   CreatePhase  `json:"phase,omitempty"` // where a failed creation stopped
	Reason  string       `json:"reason,omitempty"`
}

// CreatePhase says which step of box creation failed.
type CreatePhase string

const (
	CreatePhaseRemoveOld  CreatePhase = "remove_old" // removing resources of an earlier attempt
	CreatePhaseRepository CreatePhase = "repository" // preparing the session repositories
	CreatePhaseBox        CreatePhase = "box"        // creating the box on its host
)

// FailureCause says in owner terms why a box, an operation or a turn failed. The Reason or Error
// text beside it is detail for agents and logs; owner views show only the cause (r8).
type FailureCause string

const (
	CauseNestedDeviceMissing FailureCause = "nested_device_missing" // host lacks the TUN device for nested containers
	CauseKVMDeviceMissing    FailureCause = "kvm_device_missing"    // host lacks the KVM device
	CauseCreateFailed        FailureCause = "create_failed"         // the box could not be created on its host
	CauseStartFailed         FailureCause = "start_failed"          // the box container could not start on its host
	CauseNoHello             FailureCause = "no_hello"              // the box agent did not connect in time
	CauseEngineRestarted     FailureCause = "engine_restarted"      // the engine restarted while it created the box, or during a host run it could not follow again
	CauseRemoveFailed        FailureCause = "remove_failed"         // close could not remove the box
	CauseArchiveFailed       FailureCause = "archive_failed"        // close could not archive the session's branch (older versions)
	CauseGrantInactive       FailureCause = "grant_inactive"        // the host-exec grant is no longer active
	CauseHostLost            FailureCause = "host_lost"             // the host stopped answering during the run
	CauseHostBusy            FailureCause = "host_busy"             // stored runs only: the host execution limit was full, before host runs had connections of their own
	CauseGithubFailed        FailureCause = "github_failed"         // GitHub refused the request or did not answer
	CauseStale               FailureCause = "stale"                 // what the request needed changed before it ran
	CauseInterrupted         FailureCause = "interrupted"           // the engine stopped during the run; the outcome is unknown
	CauseInternal            FailureCause = "internal"              // an engine error; the detail is in the log
	CauseSetupFailed         FailureCause = "setup_failed"          // the box could not set up the session's repository before the turn
	CauseModelFailed         FailureCause = "model_failed"          // the model provider refused or failed the request
	CauseOutputLimit         FailureCause = "output_limit"          // the reply reached the model's output limit
	CauseNoSubmit            FailureCause = "no_submit"             // a schema step's worker ended without a valid submit
	CauseHarnessFailed       FailureCause = "harness_failed"        // the agent program stopped or could not run the turn
	CauseSetModelFailed      FailureCause = "set_model_failed"      // the agent program refused the session's new model
	CauseNotStarted          FailureCause = "not_started"           // on a stopped turn: it stopped before Pi took its inputs, so they stay pending
	CauseClosed              FailureCause = "closed"                // on a stopped turn: its session was closing, and the close ended the agent program under it
	CauseRunTimeout          FailureCause = "run_timeout"           // a subscription's Run reached its time limit
	CauseWorkflowFailed      FailureCause = "workflow_failed"       // a workflow's script threw, or its script host could not run it
)

// PressureKind is "high" for memory throttling that lasts 60 s, or "max" for a
// reached memory.max or an OOM kill.
type PressureKind string

const (
	PressureHigh PressureKind = "high"
	PressureMax  PressureKind = "max"
)

// EngineRestarted reports a new engine process to every open root. DownMillis is zero when unknown.
type EngineRestarted struct {
	Build         string `json:"build"`
	PreviousBuild string `json:"previousBuild"`
	DownMillis    int64  `json:"downMillis"`
	Clean         bool   `json:"clean"`
}

func (EngineRestarted) EventType() EventType { return "engine.restarted" }
func (EngineRestarted) isEventBody()         {}

// HostDiskLow is a host running low on disk.
type HostDiskLow struct {
	Host       HostName `json:"host"`
	FreeBytes  int64    `json:"freeBytes"`
	FloorBytes int64    `json:"floorBytes"`
}

func (HostDiskLow) EventType() EventType { return "host.disk_low" }
func (HostDiskLow) isEventBody()         {}

// BoxPressure reports memory pressure in a box. It wakes nobody.
type BoxPressure struct {
	Box  BoxID        `json:"box"`
	Kind PressureKind `json:"kind"`
}

type ImageBuilt struct {
	Host    HostName `json:"host"`
	Repo    RepoName `json:"repo"`
	Name    string   `json:"name"`
	Tag     string   `json:"tag"`
	Seconds float64  `json:"seconds"`
}

type ImageFailed struct {
	Host    HostName `json:"host"`
	Repo    RepoName `json:"repo"`
	Name    string   `json:"name"`
	Tag     string   `json:"tag"`
	Reason  string   `json:"reason"`
	LogTail string   `json:"logTail"`
}

type BoxState string

const (
	BoxPending  BoxState = "pending"
	BoxCreating BoxState = "creating"
	BoxStarting BoxState = "starting"
	BoxReady    BoxState = "ready"
	BoxStopped  BoxState = "stopped"
	BoxClosing  BoxState = "closing"
	BoxDeleted  BoxState = "deleted"
	BoxFailed   BoxState = "failed"
	BoxLost     BoxState = "lost"
)

type BoxSizeName string

const (
	BoxSizeAgent  BoxSizeName = "agent"
	BoxSizeSmall  BoxSizeName = "small"
	BoxSizeMedium BoxSizeName = "medium"
	BoxSizeLarge  BoxSizeName = "large"
	BoxSizeXLarge BoxSizeName = "xlarge"
)

type BoxSize struct {
	Name     BoxSizeName `json:"name"`
	CPUs     int         `json:"cpus"`
	MemBytes int64       `json:"memBytes"`
	Pids     int         `json:"pids"`
}

func (PostSent) EventType() EventType { return "post.sent" }
func (PostSent) isEventBody()         {}

func (PostCreated) EventType() EventType { return "post.created" }
func (PostCreated) isEventBody()         {}

func (SessionStarted) EventType() EventType { return "session.started" }
func (SessionStarted) isEventBody()         {}

func (SessionRestarted) EventType() EventType { return "session.restarted" }
func (SessionRestarted) isEventBody()         {}

func (SessionMoved) EventType() EventType { return "session.moved" }
func (SessionMoved) isEventBody()         {}

func (SessionClosed) EventType() EventType    { return "session.closed" }
func (SessionClosed) isEventBody()            {}
func (SessionCompacted) EventType() EventType { return "session.compacted" }
func (SessionCompacted) isEventBody()         {}

func (ToolFailed) EventType() EventType { return "tool.failed" }
func (ToolFailed) isEventBody()         {}

func (TurnStarted) EventType() EventType { return "turn.started" }
func (TurnStarted) isEventBody()         {}

func (TurnEnded) EventType() EventType { return "turn.ended" }
func (TurnEnded) isEventBody()         {}

func (TranscriptChanged) EventType() EventType { return "transcript.changed" }
func (TranscriptChanged) isEventBody()         {}

func (TurnActivity) EventType() EventType { return "turn.activity" }
func (TurnActivity) isEventBody()         {}

func (ShellJobEnded) EventType() EventType   { return "shell.job.ended" }
func (ShellJobEnded) isEventBody()           {}
func (ShellJobRunning) EventType() EventType { return "shell.job.running" }
func (ShellJobRunning) isEventBody()         {}

func (ImageBuilt) EventType() EventType { return "image.built" }
func (ImageBuilt) isEventBody()         {}

func (ImageFailed) EventType() EventType { return "image.failed" }
func (ImageFailed) isEventBody()         {}

func (ComputerChanged) EventType() EventType { return "computer.changed" }
func (ComputerChanged) isEventBody()         {}

func (BoxChanged) EventType() EventType { return "box.changed" }
func (BoxChanged) isEventBody()         {}

func (BoxPressure) EventType() EventType { return "box.pressure" }
func (BoxPressure) isEventBody()         {}

const (
	TurnCompleted TurnOutcome = "completed"
	TurnFailed    TurnOutcome = "failed"
	TurnStopped   TurnOutcome = "stopped"
	TurnClosed    TurnOutcome = "closed"
)

func (ConversationRead) EventType() EventType { return "conversation.read" }
func (ConversationRead) isEventBody()         {}
