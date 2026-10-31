package contract

import "time"

type Method string

const (
	MethodRuleAdd          Method = "rule.add"
	MethodRuleRemove       Method = "rule.remove"
	MethodRuleList         Method = "rule.list"
	MethodWebLogin         Method = "web.login"
	MethodConversationRead Method = "conversation.read"
	MethodRoomList         Method = "room.list"
	MethodSessionList      Method = "session.list"
	MethodArchiveDone      Method = "archive.done"
	MethodDeliveryDone     Method = "delivery.done"
	MethodHostAdd          Method = "host.add"
	MethodHostList         Method = "host.list"
	MethodAccountsQuota    Method = "accounts.quota"
	MethodRoomCreate       Method = "room.create"
	MethodRoomRepoAdd      Method = "room.repo.add"
	MethodRoomRepos        Method = "room.repos"
	MethodSessionStart     Method = "session.start"
	MethodSessionClose     Method = "session.close"
	MethodSessionMove      Method = "session.move"
	MethodPost             Method = "post"
	MethodEventsList       Method = "events.list"
	MethodBoxHello         Method = "box.hello"
	MethodComputerChanged  Method = "computer.changed"
	MethodTurnOutput       Method = "turn.output"
	MethodTurnActivity     Method = "turn.activity"
	MethodTranscriptAppend Method = "transcript.append"
	MethodShellJobEnded    Method = "shell.job.ended"
	MethodShellOutput      Method = "shell.output"
)

type RoomRule struct {
	Number int       `json:"number"`
	Text   string    `json:"text"`
	By     Principal `json:"by"`
	At     time.Time `json:"at"`
}
type RuleAddRequest struct {
	Room RoomID `json:"room"`
	Text string `json:"text"`
}
type RuleAddResponse struct {
	Number int `json:"number"`
}
type RuleRemoveRequest struct {
	Room   RoomID `json:"room"`
	Number int    `json:"number"`
}
type RuleRemoveResponse struct{}
type RuleListRequest struct {
	Room RoomID `json:"room"`
}
type RuleListResponse struct {
	Rules []RoomRule `json:"rules"`
}

type ErrorCode string

const (
	ErrInvalid       ErrorCode = "invalid"
	ErrNotFound      ErrorCode = "not_found"
	ErrForbidden     ErrorCode = "forbidden"
	ErrConflict      ErrorCode = "conflict"
	ErrMoveBusy      ErrorCode = "move-busy"
	ErrHasChildren   ErrorCode = "has_children"
	ErrParentClosing ErrorCode = "parent_closing"
	ErrStale         ErrorCode = "stale"
	ErrUnavailable   ErrorCode = "unavailable"
	ErrStoreFromV1   ErrorCode = "store_from_v1"
	ErrStoreTooOld   ErrorCode = "store_too_old"
)

type Error struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

type ConversationReadRequest struct {
	Room         RoomID       `json:"room"`
	Conversation Conversation `json:"conversation"`
	Seq          int64        `json:"seq"`
}
type ConversationReadResponse struct{}

type RoomListRequest struct{}
type RoomSummary struct {
	ID       RoomID      `json:"id"`
	Name     string      `json:"name"`
	Created  time.Time   `json:"created"`
	LastRead int64       `json:"lastRead"`
	Waiting  []OpID      `json:"waiting"` // pending owner-class operations, which only the owner decides
	Working  []SessionID `json:"working"` // open sessions with a running turn
	// Latest is the newest current-turn activity among the working sessions; none when no working session reported one.
	Latest *RoomActivity `json:"latest,omitempty"`
}
type RoomActivity struct {
	Session SessionID `json:"session"`
	Name    string    `json:"name"`
	Label   string    `json:"label"`
}
type RoomListResponse struct {
	Rooms []RoomSummary `json:"rooms"`
	// Seq is the newest event in any room when the list was read; the owner stream resumes after it.
	Seq int64 `json:"seq"`
}
type SessionListRequest struct {
	Room RoomID `json:"room"`
}
type SessionSummary struct {
	LastRead    int64         `json:"lastRead"`
	ID          SessionID     `json:"id"`
	Name        string        `json:"name"`
	Parent      SessionID     `json:"parent"`
	Profile     ProfileName   `json:"profile"`
	Box         BoxID         `json:"box"`
	BoxState    BoxState      `json:"boxState"`
	Computer    ComputerState `json:"computer"`
	Goal        string        `json:"goal"`
	Turn        TurnID        `json:"turn,omitempty"`        // the running turn
	Activity    string        `json:"activity,omitempty"`    // the running turn's latest TurnActivity label
	LastOutcome TurnOutcome   `json:"lastOutcome,omitempty"` // of the last ended turn that was not stopped; a stopped turn neither fails nor succeeds
	ShellJobs   int           `json:"shellJobs,omitempty"`   // TurnEnded.ShellJobs of the last ended turn
}
type SessionListResponse struct {
	Sessions []SessionSummary `json:"sessions"`
	// Seq is the room's newest event when the list was read; live events after it update the list.
	Seq int64 `json:"seq"`
}

type WebLoginRequest struct{}
type WebLoginResponse struct {
	URL string `json:"url"`
}

type AccountsQuotaRequest struct{}
type AccountsQuotaResponse struct {
	Accounts []AccountUsage `json:"accounts"`
}

// UsageCause is empty when usage was read successfully.
type UsageCause string

const (
	UsageOK            UsageCause = ""
	UsageSignInExpired UsageCause = "sign_in_expired"
	UsageUnreachable   UsageCause = "unreachable"
)

type AccountUsage struct {
	Provider string        `json:"provider"`
	Label    string        `json:"label"`
	Windows  []UsageWindow `json:"windows"`
	Cause    UsageCause    `json:"cause"`
}
type UsageWindow struct {
	Name        string    `json:"name"`
	UsedPercent float64   `json:"usedPercent"`
	Seconds     int64     `json:"seconds"`
	ResetsAt    time.Time `json:"resetsAt"`
}

type HostAddRequest struct {
	Name           HostName `json:"name"`
	SSH            string   `json:"ssh"`
	OwnerOnlyAdmin bool     `json:"ownerOnlyAdmin"`
}
type HostAddResponse struct{}

// HostListRequest asks for the hosts an owner terminal can open (5.15). Only the owner calls it.
type HostListRequest struct{}
type HostListResponse struct {
	Hosts []HostSummary `json:"hosts"` // ControlPlane first, then each added host by name
}

type HostSummary struct {
	Name      HostName        `json:"name"`
	Reachable bool            `json:"reachable"`
	Boxes     int             `json:"boxes"`
	CPUs      int             `json:"cpus"`
	MemBytes  int64           `json:"memBytes"`
	CPUUsed   float64         `json:"cpuUsed"`
	MemUsed   int64           `json:"memUsed"`
	DiskBytes int64           `json:"diskBytes"`
	DiskUsed  int64           `json:"diskUsed"`
	LastSeen  time.Time       `json:"lastSeen,omitzero"`
	History   []HostLoadPoint `json:"history"`
}

type HostLoadPoint struct {
	At      time.Time `json:"at"`
	CPUUsed float64   `json:"cpuUsed"`
	MemUsed int64     `json:"memUsed"`
}

type RoomCreateRequest struct {
	Name        string      `json:"name"`
	RootProfile ProfileName `json:"rootProfile"`
}
type RoomCreateResponse struct {
	Room RoomID `json:"room"`
}
type RoomRepoAddRequest struct {
	Room    RoomID   `json:"room"`
	Repo    RepoName `json:"repo"`
	Default bool     `json:"default"`
}
type RoomRepoAddResponse struct{}
type RoomReposRequest struct {
	Room RoomID `json:"room"`
}
type RoomReposResponse struct {
	Repos []RoomRepo `json:"repos"`
}
type SessionStartRequest struct {
	Room    RoomID      `json:"room"`
	Name    string      `json:"name"`
	Parent  SessionID   `json:"parent,omitempty"`
	Repo    RepoName    `json:"repo,omitempty"`
	Profile ProfileName `json:"profile"`
	Goal    string      `json:"goal,omitempty"`
	Brief   string      `json:"brief,omitempty"`
}
type SessionStartResponse struct {
	Session SessionID `json:"session"`
	Box     BoxID     `json:"box"`
}
type SessionMoveRequest struct {
	Session   SessionID `json:"session"`
	NewParent SessionID `json:"newParent"`
}
type SessionMoveResponse struct{}

type SessionCloseRequest struct {
	Session SessionID `json:"session"`
	Discard bool      `json:"discard,omitempty"`
}
type SessionCloseResponse struct{}
type PostRequest struct {
	Room        RoomID       `json:"room"`
	Text        string       `json:"text"`
	Mentions    []string     `json:"mentions,omitempty"`
	Home        Conversation `json:"home,omitempty"`
	ReplyTo     EventID      `json:"replyTo,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}
type PostResponse struct {
	Event EventID `json:"event"`
	Seq   int64   `json:"seq"`
}
type EventsListRequest struct {
	Room      RoomID      `json:"room"`
	AfterSeq  int64       `json:"afterSeq"`
	BeforeSeq int64       `json:"beforeSeq,omitempty"`
	Limit     int         `json:"limit"`
	Types     []EventType `json:"types,omitempty"`
	// Home, a DM, keeps only events whose home is that DM: its posts and the turns that answered in it.
	Home *Conversation `json:"home,omitempty"`
}
type EventsListResponse struct {
	Events []Event              `json:"events"`
	Names  map[SessionID]string `json:"names"`
	Next   int64                `json:"next"`
}
type BoxHelloRequest struct {
	Version string `json:"version"`
}
type ModelProvider string

const (
	ModelProviderAnthropic ModelProvider = "anthropic"
	ModelProviderOpenAI    ModelProvider = "openai"
)

type ThinkingLevel string

const (
	ThinkingOff     ThinkingLevel = "off"
	ThinkingMinimal ThinkingLevel = "minimal"
	ThinkingLow     ThinkingLevel = "low"
	ThinkingMedium  ThinkingLevel = "medium"
	ThinkingHigh    ThinkingLevel = "high"
	ThinkingXHigh   ThinkingLevel = "xhigh"
)

type ModelChoice struct {
	Provider ModelProvider `json:"provider"`
	ID       string        `json:"id"`
	Thinking ThinkingLevel `json:"thinking"`
}

type BoxRepo struct {
	Name   RepoName `json:"name"`
	Cache  string   `json:"cache"`
	Branch string   `json:"branch"`
	Base   string   `json:"base"`
}

type BoxHelloResponse struct {
	Session SessionID   `json:"session"`
	Box     BoxID       `json:"box"`
	Model   ModelChoice `json:"model"`
	Repos   []BoxRepo   `json:"repos,omitempty"`
	Rules   []RoomRule  `json:"rules,omitempty"`
	Upgrade string      `json:"upgrade,omitempty"`
}
type TurnOutputRequest struct {
	ShellJobs int          `json:"shellJobs"`
	Error     string       `json:"error,omitempty"`
	Cause     FailureCause `json:"cause,omitempty"`
	Turn      TurnID       `json:"turn"`
	Outcome   TurnOutcome  `json:"outcome"`
	Final     string       `json:"final,omitempty"`
	Model     string       `json:"model"`
	Usage     Usage        `json:"usage"`
}
type TurnOutputResponse struct{}

// TurnActivityRequest reports the running turn's latest tool call. The box agent sends at most
// one a second. Readers show a label only while its turn runs.
type TurnActivityRequest struct {
	Turn  TurnID `json:"turn"`
	Label string `json:"label"`
}
type TurnActivityResponse struct{}
type TranscriptAppendRequest struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	Bytes  []byte `json:"bytes"`
}
type TranscriptAppendResponse struct {
	AckedOffset int64 `json:"ackedOffset"`
}
type ShellJobEndedRequest struct {
	Job      ShellJobID `json:"job"`
	ExitCode int        `json:"exitCode"`
	Tail     string     `json:"tail"` // the last output, masked
}
type ShellJobEndedResponse struct{}
type TurnInput struct {
	Delivery DeliverySeq  `json:"delivery"`
	Surface  Surface      `json:"surface"`
	Reply    ReplyChannel `json:"reply"`
	Text     string       `json:"text"`
}
type TurnStart struct {
	Turn   TurnID      `json:"turn"`
	Inputs []TurnInput `json:"inputs"`
}
type TurnQueue string

const (
	QueueSteer    TurnQueue = "steer"
	QueueFollowUp TurnQueue = "follow_up"
)

type TurnSteer struct {
	Input TurnInput `json:"input"`
	Queue TurnQueue `json:"queue"`
}
type TurnStop struct {
	Turn   TurnID `json:"turn"`
	Reason string `json:"reason"`
}
type Upgrade struct {
	Version string `json:"version"`
}

type ComputerWatchStart struct {
	Generation uint64 `json:"generation"`
}
type ComputerWatchStop struct{}

// ShellWatchStart asks the box agent to post each shell job's output with shell.output while the
// owner watches. Each start begins a new generation, and its first post resets every job's view.
type ShellWatchStart struct {
	Generation uint64 `json:"generation"`
}
type ShellWatchStop struct{}

// ShellOutputRequest carries the new output of the box's shell jobs, about every 250 ms while watched.
// The engine drops a post from an older generation.
type ShellOutputRequest struct {
	Generation uint64           `json:"generation"`
	Jobs       []ShellJobOutput `json:"jobs"`
}
type ShellOutputResponse struct{}

// ShellJobOutput is one job's output since the last post, masked, in whole lines. With Reset, Text is
// the job's last 64 KiB and replaces what the viewer shows. GET /v1/shell/watch streams these values.
type ShellJobOutput struct {
	Job      ShellJobID `json:"job"`
	Command  string     `json:"command"`
	Running  bool       `json:"running"`
	ExitCode int        `json:"exitCode"` // when Running is false
	Text     string     `json:"text"`
	Reset    bool       `json:"reset,omitempty"`
	Started  time.Time  `json:"started"`        // by the box's clock
	Ended    time.Time  `json:"ended,omitzero"` // by the box's clock, once the job has ended
}

// TerminalResize is the JSON text frame a client sends on GET /v1/terminal to set the terminal's size.
// Binary frames carry the terminal's bytes both ways.
type TerminalResize struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// TerminalExit is the engine's last JSON text frame on GET /v1/terminal, sent when the shell exits.
type TerminalExit struct {
	ExitCode int `json:"exitCode"`
}

type ComputerChangedRequest struct {
	To ComputerState `json:"to"`
}
type ComputerChangedResponse struct{}

type Archive struct{}

type ArchiveDoneRequest struct {
	Repo    RepoName `json:"repo"`
	Commit  string   `json:"commit"`
	Skipped bool     `json:"skipped"`
	Error   string   `json:"error,omitempty"`
}
type ArchiveDoneResponse struct{}

type DeliverySeq int64
type DeliveryDoneRequest struct {
	Delivery DeliverySeq `json:"delivery"`
}
type DeliveryDoneResponse struct{}

type BoxMessageType string
type BoxMessageBody interface {
	BoxMessageType() BoxMessageType
	isBoxMessageBody()
}
type BoxMessage struct {
	Body BoxMessageBody `json:"body"`
}

func (ComputerWatchStart) BoxMessageType() BoxMessageType { return "computer.watch.start" }
func (ComputerWatchStart) isBoxMessageBody()              {}
func (ComputerWatchStop) BoxMessageType() BoxMessageType  { return "computer.watch.stop" }
func (ComputerWatchStop) isBoxMessageBody()               {}
func (ShellWatchStart) BoxMessageType() BoxMessageType    { return "shell.watch.start" }
func (ShellWatchStart) isBoxMessageBody()                 {}
func (ShellWatchStop) BoxMessageType() BoxMessageType     { return "shell.watch.stop" }
func (ShellWatchStop) isBoxMessageBody()                  {}

func (Archive) BoxMessageType() BoxMessageType { return "archive" }
func (Archive) isBoxMessageBody()              {}

func (TurnStart) BoxMessageType() BoxMessageType { return "turn.start" }
func (TurnStart) isBoxMessageBody()              {}
func (TurnSteer) BoxMessageType() BoxMessageType { return "turn.steer" }
func (TurnSteer) isBoxMessageBody()              {}
func (TurnStop) BoxMessageType() BoxMessageType  { return "turn.stop" }
func (TurnStop) isBoxMessageBody()               {}
func (Upgrade) BoxMessageType() BoxMessageType   { return "upgrade" }
func (Upgrade) isBoxMessageBody()                {}
