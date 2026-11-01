package contract

import "time"

// StreamHello is sent before events on each stream connection.
type StreamHello struct {
	Build string `json:"build"`
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

type PostCreated struct {
	Text        string       `json:"text"`
	Turn        TurnID       `json:"turn,omitempty"`
	Mentions    []Principal  `json:"mentions,omitempty"`
	Home        Conversation `json:"home"` // feed or a DM
	ReplyTo     EventID      `json:"replyTo,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	ClientID    string       `json:"clientId,omitempty"` // the sender's PostRequest.ClientID, so a pending row is replaced exactly
	ToolCallID  string       `json:"toolCallId,omitempty"`
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

type SessionStarted struct {
	Session SessionID   `json:"session"`
	Name    string      `json:"name"`
	Parent  SessionID   `json:"parent,omitempty"` // empty for root
	Profile ProfileName `json:"profile"`
	Goal    string      `json:"goal,omitempty"`
	Brief   string      `json:"brief,omitempty"`
}

type SessionRestarted struct {
	Session SessionID `json:"session"`
	// ImageUpdated marks a new container on the same volume, as opposed to a box restarted from nothing.
	ImageUpdated bool `json:"imageUpdated,omitempty"`
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

type SessionClosed struct {
	Reason  SessionCloseReason `json:"reason,omitempty"`
	Session SessionID          `json:"session"`
	Archive string             `json:"archive,omitempty"` // refs/dyna/archive/<session>, if any
	Unheld  []GoalID           `json:"unheld,omitempty"`  // M3: open goals left with no holder, because the parent is root (7.4)
}

type TurnStarted struct {
	Session SessionID `json:"session"`
	Turn    TurnID    `json:"turn"`
	Inputs  []EventID `json:"inputs"` // the deliveries this turn takes
}

type TurnOutcome string // "completed" | "failed" | "stopped"

type TurnEnded struct {
	Session SessionID    `json:"session"`
	Turn    TurnID       `json:"turn"`
	Outcome TurnOutcome  `json:"outcome"`
	Final   string       `json:"final,omitempty"`   // the final response
	Replies []FinalReply `json:"replies,omitempty"` // where Final was delivered
	Usage   Usage        `json:"usage"`
	Error   string       `json:"error,omitempty"` // failed only
	Cause   FailureCause `json:"cause,omitempty"` // failed only
	// ShellJobs counts the background shell jobs still running when the turn ended. Each job's
	// end wakes the session, so the next turn.ended carries the new count.
	ShellJobs int `json:"shellJobs,omitempty"`
	// EndPosts carries the harness's result to TurnOutputRequest.EndPosts. The event records the same fact in Replies.
	EndPosts []EventID `json:"-"`
}

type ConversationRead struct {
	Room         RoomID       `json:"room"`
	Conversation Conversation `json:"conversation"`
	Seq          int64        `json:"seq"`
}

// TurnActivity is live only. The engine keeps each running turn's latest label in memory and
// never stores this event, so it has seq 0, events.list never returns it, and it does not move a
// stream's resume point. turn.ended clears it.
type TurnActivity struct {
	Session SessionID `json:"session"`
	Turn    TurnID    `json:"turn"`
	Label   string    `json:"label"` // the running tool call, masked, one line of at most 80 characters
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
	Description string       `json:"description,omitempty"`
	End         *ShellJobEnd `json:"end,omitempty"`
	Session     SessionID    `json:"session"`
	Job         ShellJobID   `json:"job"`
	ExitCode    int          `json:"exitCode"`
	Tail        string       `json:"tail"`
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

type BoxChanged struct {
	Box     BoxID        `json:"box"`
	Session SessionID    `json:"session"`
	Host    HostName     `json:"host,omitempty"`
	From    BoxState     `json:"from,omitempty"` // empty for a new box
	To      BoxState     `json:"to"`
	Cause   FailureCause `json:"cause,omitempty"` // a failed box, or a close that could not archive
	Reason  string       `json:"reason,omitempty"`
}

// FailureCause says in owner terms why a box, an operation or a turn failed. The Reason or Error
// text beside it is detail for agents and logs; owner views show only the cause (r8).
type FailureCause string

const (
	CauseNestedDeviceMissing FailureCause = "nested_device_missing" // host lacks the TUN device for nested containers
	CauseKVMDeviceMissing    FailureCause = "kvm_device_missing"    // host lacks the KVM device
	CauseCreateFailed        FailureCause = "create_failed"         // the box could not be created on its host
	CauseStartFailed         FailureCause = "start_failed"          // the box container could not start on its host
	CauseNoHello             FailureCause = "no_hello"              // the box agent did not connect in time
	CauseEngineRestarted     FailureCause = "engine_restarted"      // the engine restarted while it created the box
	CauseRemoveFailed        FailureCause = "remove_failed"         // close could not remove the box
	CauseArchiveFailed       FailureCause = "archive_failed"        // close could not archive the session's branch
	CauseGrantInactive       FailureCause = "grant_inactive"        // the host-exec grant is no longer active
	CauseHostLost            FailureCause = "host_lost"             // the host stopped answering during the run
	CauseHostBusy            FailureCause = "host_busy"             // the host execution limit is full
	CauseGithubFailed        FailureCause = "github_failed"         // GitHub refused the request or did not answer
	CauseStale               FailureCause = "stale"                 // what the request needed changed before it ran
	CauseInterrupted         FailureCause = "interrupted"           // the engine stopped during the run; the outcome is unknown
	CauseInternal            FailureCause = "internal"              // an engine error; the detail is in the log
	CauseSetupFailed         FailureCause = "setup_failed"          // the box could not set up the session's repository before the turn
	CauseModelFailed         FailureCause = "model_failed"          // the model provider refused or failed the request
	CauseOutputLimit         FailureCause = "output_limit"          // the reply reached the model's output limit
	CauseHarnessFailed       FailureCause = "harness_failed"        // the agent program stopped or could not run the turn
	CauseSetModelFailed      FailureCause = "set_model_failed"      // the agent program refused the session's new model
	CauseNotStarted          FailureCause = "not_started"           // on a stopped turn: it stopped before Pi took its inputs, so they stay pending
)

// PressureKind is "high" for memory throttling that lasts 60 s, or "max" for a
// reached memory.max or an OOM kill.
type PressureKind string

const (
	PressureHigh PressureKind = "high"
	PressureMax  PressureKind = "max"
)

// BoxPressure reports memory pressure in a box. It wakes nobody.
type HostDiskLow struct {
	Host       HostName `json:"host"`
	FreeBytes  int64    `json:"freeBytes"`
	FloorBytes int64    `json:"floorBytes"`
}

func (HostDiskLow) EventType() EventType { return "host.disk_low" }
func (HostDiskLow) isEventBody()         {}

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
)

type BoxSize struct {
	Name     BoxSizeName `json:"name"`
	CPUs     int         `json:"cpus"`
	MemBytes int64       `json:"memBytes"`
	Pids     int         `json:"pids"`
}

func (PostCreated) EventType() EventType { return "post.created" }
func (PostCreated) isEventBody()         {}

func (SessionStarted) EventType() EventType { return "session.started" }
func (SessionStarted) isEventBody()         {}

func (SessionRestarted) EventType() EventType { return "session.restarted" }
func (SessionRestarted) isEventBody()         {}

func (SessionMoved) EventType() EventType { return "session.moved" }
func (SessionMoved) isEventBody()         {}

func (SessionClosed) EventType() EventType { return "session.closed" }
func (SessionClosed) isEventBody()         {}

func (TurnStarted) EventType() EventType { return "turn.started" }
func (TurnStarted) isEventBody()         {}

func (TurnEnded) EventType() EventType { return "turn.ended" }
func (TurnEnded) isEventBody()         {}

func (TranscriptChanged) EventType() EventType { return "transcript.changed" }
func (TranscriptChanged) isEventBody()         {}

func (TurnActivity) EventType() EventType { return "turn.activity" }
func (TurnActivity) isEventBody()         {}

func (ShellJobEnded) EventType() EventType { return "shell.job.ended" }
func (ShellJobEnded) isEventBody()         {}

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
)

func (ConversationRead) EventType() EventType { return "conversation.read" }
func (ConversationRead) isEventBody()         {}
