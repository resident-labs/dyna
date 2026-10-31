package contract

import "time"

// OpKind is "family.verb", such as "hostexec.run" or "repo.settings.set".
type OpKind string

type Class string

const (
	ClassAuto  Class = "auto"  // runs at once
	ClassAgent Class = "agent" // a session above the caller, or the owner, approves
	ClassOwner Class = "owner" // only the owner approves
)

type OpState string // "pending" | "approved" | "denied" | "expired" | "withdrawn" | "done" | "failed"

type Operation struct {
	ID       OpID      `json:"id"`
	Kind     OpKind    `json:"kind"`
	Room     RoomID    `json:"room"`
	By       Principal `json:"by"`
	Class    Class     `json:"class"`
	Card     Card      `json:"card"`
	Args     OpArgs    `json:"args"` // sealed union, one struct per kind
	State    OpState   `json:"state"`
	Created  time.Time `json:"created"`
	Expires  time.Time `json:"expires"`
	Decision *Decision `json:"decision,omitempty"`
	Result   *OpResult `json:"result,omitempty"`
}

type OpArgs interface {
	OpKind() OpKind
	isOpArgs()
}

type TargetKind string  // "repo" | "room" | "host" | "session" | "account"
type ChangeField string // declared per operation kind

type Card struct {
	Title   string   `json:"title"`
	Body    string   `json:"body,omitempty"`
	Target  Target   `json:"target"`
	Changes []Change `json:"changes,omitempty"`
	Command []string `json:"command,omitempty"` // argv, for host exec
}

type Target struct {
	Kind TargetKind `json:"kind"`
	Name string     `json:"name"`
}

type Change struct {
	Field ChangeField `json:"field"`
	Old   string      `json:"old"`
	New   string      `json:"new"`
}

type Decision struct {
	By       Principal `json:"by"`
	Approved bool      `json:"approved"`
	Reason   string    `json:"reason,omitempty"`
	At       time.Time `json:"at"`
}

type PrOpenArgs struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Base  string `json:"base"`
	Draft bool   `json:"draft"`
}

func (PrOpenArgs) OpKind() OpKind { return "pr.open" }
func (PrOpenArgs) isOpArgs()      {}

type PrOpened struct {
	Repo    RepoName  `json:"repo"`
	Number  int       `json:"number"`
	Session SessionID `json:"session"`
	URL     string    `json:"url"`
}

func (PrOpened) EventType() EventType { return "pr.opened" }
func (PrOpened) isEventBody()         {}

type PrOpenResult struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

type OpResult struct {
	PR         *PrOpenResult `json:"pr,omitempty"`
	ExitCode   int           `json:"exitCode"`
	Cause      FailureCause  `json:"cause,omitempty"` // set with Error
	Error      string        `json:"error,omitempty"`
	Output     string        `json:"output,omitempty"` // up to 64 KiB inline
	Truncated  bool          `json:"truncated,omitempty"`
	OutputFile string        `json:"outputFile,omitempty"` // the rest, by hash
}

const (
	OpPending   OpState = "pending"
	OpApproved  OpState = "approved"
	OpDenied    OpState = "denied"
	OpExpired   OpState = "expired"
	OpWithdrawn OpState = "withdrawn"
	OpDone      OpState = "done"
	OpFailed    OpState = "failed"
)
const (
	TargetRepo    TargetKind = "repo"
	TargetRoom    TargetKind = "room"
	TargetHost    TargetKind = "host"
	TargetSession TargetKind = "session"
	TargetAccount TargetKind = "account"
)
