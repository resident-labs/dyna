package roomsvc

import (
	"time"

	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/terminalsvc"
	"dyna-agent/internal/workeroutput"
)

// Session is a member's drill-down: identity plus transcript.
type Session struct {
	Cursor  string `json:"cursor,omitempty"`
	Earlier bool   `json:"earlier"`
	Newer   bool   `json:"newer"`
	Member  Member `json:"member"`
	Address string `json:"address"`
	Posts   []Post `json:"posts" tstype:"Post[] | null"`
	// References are the compact rows of the posts of other conversations that
	// this one received or wrote. Each row names its post, its sender and its
	// home, so a reader jumps to the post where it lives instead of drawing a
	// second copy.
	References []ReferenceRow     `json:"references,omitempty"`
	Media      []Post             `json:"media" tstype:"Post[] | null"`
	Dir        string             `json:"dir"`
	Usage      workeroutput.Usage `json:"usage"`
	Turns      []Turn             `json:"turns" tstype:"Turn[] | null"`
}

// ReferenceRow is one post of another conversation, as a session's DM shows it:
// who wrote it, which conversation it is in, its typed event facts or text
// preview, and how many files it carries. TS is unix milliseconds.
type ReferenceRow struct {
	PostID string           `json:"postId"`
	From   string           `json:"from"`
	Kind   string           `json:"kind,omitempty"`
	Notice room.EventNotice `json:"notice,omitzero"`
	// Payload keeps a typed event's facts so the DM can use the same card as
	// the feed instead of reading the agent-facing text as a preview.
	Payload room.EventPayload `json:"payload,omitzero"`
	// Speaker is the sender's label (see SpeakerLabel).
	Speaker string `json:"speaker"`
	// FromHue is the sender's stable name color index (see NameHue).
	FromHue int `json:"fromHue"`
	// Conversation is the home of the post, such as "room" or "voice".
	Conversation string `json:"conversation"`
	TS           int64  `json:"ts"`
	Preview      string `json:"preview"`
	Attachments  int    `json:"attachments"`
}

// Turn is one transcript turn.
type Turn struct {
	WrapUp             bool                            `json:"wrapUp,omitempty"`
	Harness            string                          `json:"harness,omitempty"`
	OutputDialect      string                          `json:"outputDialect,omitempty"`
	UsageNormalization workeroutput.UsageNormalization `json:"usageNormalization"`
	Profile            string                          `json:"profile,omitempty"`
	Model              string                          `json:"model,omitempty"`
	HarnessSession     string                          `json:"harnessSession,omitempty"`
	N                  int                             `json:"n"`
	Status             string                          `json:"status"`
	Started            time.Time                       `json:"started"`
	Ended              time.Time                       `json:"ended"`
	Usage              workeroutput.Usage              `json:"usage"`
	FirstCall          *workeroutput.CallUsage         `json:"firstCall,omitempty"`
	Items              []Item                          `json:"items" tstype:"Item[] | null"`
	Omitted            bool                            `json:"omitted,omitempty"`
	Wake               *WakeView                       `json:"wake,omitempty"`
}

// WakeView is the typed wake cause the session view draws. Line is the short
// sentence; the other fields are the facts, so the view does not parse text.
type WakeView struct {
	Kind   string `json:"kind"`
	PostID string `json:"postId,omitempty"`
	Sender string `json:"sender,omitempty"`
	Name   string `json:"name,omitempty"`
	Line   string `json:"line"`
}

// Item is one transcript entry. Kind is prompt, steering, reply, tool,
// thought, error, note or compaction.
type Item struct {
	PostID    string    `json:"postId,omitempty"`
	Delivered bool      `json:"delivered,omitempty"`
	Kind      string    `json:"kind"`
	TS        time.Time `json:"ts"`
	Text      string    `json:"text,omitempty"`
	From      string    `json:"from,omitempty"`
	// Step is what a tool call did, in the session view's own words: read,
	// edit, search, run and so on. Label is the call's short name, a file name
	// or a command's first word, and ToolName the harness's own tool ID. The
	// row shows Step, Label and a state; ToolName, the full target and the exit
	// code stay in its details.
	Step       string `json:"step,omitempty" tstype:"StepKind"`
	Label      string `json:"label,omitempty"`
	ToolName   string `json:"toolName,omitempty"`
	DurationMS int    `json:"durationMs,omitempty"`
	Tool       string `json:"tool,omitempty"`
	Target     string `json:"target,omitempty"`
	Aside      string `json:"aside,omitempty"`
	Failed     bool   `json:"failed,omitempty"`
	Done       bool   `json:"done,omitempty"`
	// JobID and JobOp link a call of Dyna's shell tools to its job: JobOp is
	// run, output or stop. Job is the snapshot of the job a run started.
	// Command, Output and Exit explain a failed call: its input, the tail of
	// what it printed, and its exit code. Other calls carry none of them.
	Command string                `json:"command,omitempty"`
	Output  string                `json:"output,omitempty"`
	Exit    *int                  `json:"exit,omitempty"`
	JobID   string                `json:"jobId,omitempty"`
	JobOp   string                `json:"jobOp,omitempty"`
	Job     *terminalsvc.ShellJob `json:"job,omitempty"`
	// A compaction item carries its state, its trigger (auto, manual or dyna),
	// the context size on each side when reported, and the reason it failed.
	State        string `json:"state,omitempty"`
	Trigger      string `json:"trigger,omitempty"`
	TokensBefore int64  `json:"tokensBefore,omitempty"`
	TokensAfter  int64  `json:"tokensAfter,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// Profile is a member's card: identity and its newest posts. The web card
// shows the goals the member holds from the room's goal list.
type Profile struct {
	Member Member             `json:"member"`
	Parent string             `json:"parent,omitempty"` // the parent's name; empty for a top-level session
	Dir    string             `json:"dir"`
	Usage  workeroutput.Usage `json:"usage"`
	Posts  []Post             `json:"posts" tstype:"Post[] | null"` // the member's newest posts, oldest first
}

// BulkSessionOptions applies one shared choice to each selected session.
// Close hands work to an ancestor; Force permits failed handoffs. Adopt
// moves each session under Parent.
type BulkSessionOptions struct {
	Sessions []string `json:"sessions"`
	Action   string   `json:"action"`
	Profile  string   `json:"profile,omitempty"`
	Model    string   `json:"model,omitempty"`
	Force    bool     `json:"force,omitempty"`
	Parent   string   `json:"parent,omitempty"`
}

type BulkSessionItem struct {
	Session  string                  `json:"session"`
	OK       bool                    `json:"ok"`
	Error    string                  `json:"error,omitempty"`
	Close    *subagent.CloseRecovery `json:"close,omitempty"`
	Member   *Member                 `json:"member,omitempty"`
	Transfer *subagent.TransferState `json:"transfer,omitempty"`
}

type BulkSessionResult struct {
	Action    string            `json:"action"`
	Results   []BulkSessionItem `json:"results" tstype:"BulkSessionItem[] | null"`
	Succeeded int               `json:"succeeded"`
	Failed    int               `json:"failed"`
}
