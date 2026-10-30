package contract

import "time"

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

type PostCreated struct {
	Text        string       `json:"text"`
	Mentions    []Principal  `json:"mentions,omitempty"`
	Home        Conversation `json:"home"` // feed or a DM
	ReplyTo     EventID      `json:"replyTo,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

type Attachment struct {
	Name   string `json:"name"`
	Media  string `json:"media"` // MIME type
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"` // the file is stored by hash
}

type SessionStarted struct {
	Session SessionID   `json:"session"`
	Name    string      `json:"name"`
	Parent  SessionID   `json:"parent,omitempty"` // empty for root
	Profile ProfileName `json:"profile"`
	Goal    string      `json:"goal,omitempty"`
	Brief   string      `json:"brief,omitempty"`
}

type SessionClosed struct {
	Session SessionID `json:"session"`
	Archive string    `json:"archive,omitempty"` // refs/dyna/archive/<session>, if any
	Unheld  []GoalID  `json:"unheld,omitempty"`  // M3: open goals left with no holder, because the parent is root (7.4)
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
	Session  SessionID  `json:"session"`
	Job      ShellJobID `json:"job"`
	ExitCode int        `json:"exitCode"`
	Tail     string     `json:"tail"`
}

type BoxChanged struct {
	Box     BoxID     `json:"box"`
	Session SessionID `json:"session"`
	Host    HostName  `json:"host,omitempty"`
	From    BoxState  `json:"from"`
	To      BoxState  `json:"to"`
	Reason  string    `json:"reason,omitempty"`
}

// PressureKind is "high" for memory throttling that lasts 60 s, or "max" for a
// reached memory.max or an OOM kill.
type PressureKind string

const (
	PressureHigh PressureKind = "high"
	PressureMax  PressureKind = "max"
)

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

func (SessionClosed) EventType() EventType { return "session.closed" }
func (SessionClosed) isEventBody()         {}

func (TurnStarted) EventType() EventType { return "turn.started" }
func (TurnStarted) isEventBody()         {}

func (TurnEnded) EventType() EventType { return "turn.ended" }
func (TurnEnded) isEventBody()         {}

func (ShellJobEnded) EventType() EventType { return "shell.job.ended" }
func (ShellJobEnded) isEventBody()         {}

func (ImageBuilt) EventType() EventType { return "image.built" }
func (ImageBuilt) isEventBody()         {}

func (ImageFailed) EventType() EventType { return "image.failed" }
func (ImageFailed) isEventBody()         {}

func (BoxChanged) EventType() EventType { return "box.changed" }
func (BoxChanged) isEventBody()         {}

func (BoxPressure) EventType() EventType { return "box.pressure" }
func (BoxPressure) isEventBody()         {}

const (
	TurnCompleted TurnOutcome = "completed"
	TurnFailed    TurnOutcome = "failed"
	TurnStopped   TurnOutcome = "stopped"
)
