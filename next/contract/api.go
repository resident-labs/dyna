package contract

import (
	"errors"
	"time"
)

type InstructionsGetRequest struct {
	Room RoomID `json:"room,omitempty"`
}
type InstructionsGetResponse struct {
	Text    string     `json:"text"`
	Hash    string     `json:"hash"`
	SavedAt *time.Time `json:"savedAt,omitempty"`
}
type InstructionsSetRequest struct {
	Room RoomID `json:"room,omitempty"`
	Text string `json:"text"`
	Base string `json:"base"`
}
type InstructionsSetResponse struct {
	Hash string `json:"hash"`
}

type Method string

const (
	MethodInstructionsGet         Method = "instructions.get"
	MethodInstructionsSet         Method = "instructions.set"
	MethodWebLogin                Method = "web.login"
	MethodConversationRead        Method = "conversation.read"
	MethodRoomList                Method = "room.list"
	MethodSessionList             Method = "session.list"
	MethodArchiveDone             Method = "archive.done"
	MethodDeliveryDone            Method = "delivery.done"
	MethodHostAdd                 Method = "host.add"
	MethodHostList                Method = "host.list"
	MethodAccountsList            Method = "accounts.list"
	MethodAccountsConnect         Method = "accounts.connect"
	MethodAccountsReconnect       Method = "accounts.reconnect"
	MethodAccountsConnectCallback Method = "accounts.connect_callback"
	MethodAccountsConnectStatus   Method = "accounts.connect_status"
	MethodAccountsSetKey          Method = "accounts.set_key"
	MethodAccountsRemove          Method = "accounts.remove"
	MethodAccountsSetLimits       Method = "accounts.set_limits"
	MethodProfileSet              Method = "profile.set"
	MethodProfileRemove           Method = "profile.remove"
	MethodProfileList             Method = "profile.list"
	MethodAccountsQuota           Method = "accounts.quota"
	MethodRoomPause               Method = "room.pause"
	MethodRoomSetLimits           Method = "room.set_limits"
	MethodRoomCreate              Method = "room.create"
	MethodRoomRepoAdd             Method = "room.repo.add"
	MethodRoomRepos               Method = "room.repos"
	MethodSessionStart            Method = "session.start"
	MethodSessionClose            Method = "session.close"
	MethodSessionStop             Method = "session.stop"
	MethodSessionSetProfile       Method = "session.set_profile"
	MethodSessionRestart          Method = "session.restart"
	MethodSessionMove             Method = "session.move"
	MethodPost                    Method = "post"
	MethodPostSearch              Method = "post.search"
	MethodSessionSearch           Method = "session.search"
	MethodEventsList              Method = "events.list"
	MethodBoxHello                Method = "box.hello"
	MethodComputerChanged         Method = "computer.changed"
	MethodComputerInput           Method = "computer.input"
	MethodTurnOutput              Method = "turn.output"
	MethodTurnActivity            Method = "turn.activity"
	MethodTranscriptFetch         Method = "transcript.fetch"
	MethodTranscriptAppend        Method = "transcript.append"
	MethodTranscriptRead          Method = "transcript.read"
	MethodSessionWork             Method = "session.work"
	MethodShellJobEnded           Method = "shell.job.ended"
	MethodShellOutput             Method = "shell.output"
)

type ErrorCode string

const (
	ErrPodmanRequired       ErrorCode = "podman_required"
	ErrInvalid              ErrorCode = "invalid"
	ErrUnknownAction        ErrorCode = "unknown_action"
	ErrNotFound             ErrorCode = "not_found"
	ErrForbidden            ErrorCode = "forbidden"
	ErrConflict             ErrorCode = "conflict"
	ErrProfileExists        ErrorCode = "profile_exists"
	ErrMoveBusy             ErrorCode = "move_busy"
	ErrHasChildren          ErrorCode = "has_children"
	ErrParentClosing        ErrorCode = "parent_closing"
	ErrSessionClosing       ErrorCode = "session_closing"
	ErrStale                ErrorCode = "stale"
	ErrVisualSuperseded     ErrorCode = "visual_superseded"
	ErrUnavailable          ErrorCode = "unavailable"
	ErrReconnectUnavailable ErrorCode = "reconnect_unavailable"
	ErrStoreFromV1          ErrorCode = "store_from_v1"
	ErrStoreTooOld          ErrorCode = "store_too_old"
)

type Error struct {
	Code    ErrorCode         `json:"code"`
	Message string            `json:"message"`
	Visual  *VisualSuperseded `json:"visual,omitempty"`
}

type VisualSuperseded struct {
	Event   EventID `json:"event"`
	Version int     `json:"version"`
	Seq     int64   `json:"seq"`
}

type ConversationReadRequest struct {
	Room         RoomID       `json:"room"`
	Conversation Conversation `json:"conversation"`
	Seq          int64        `json:"seq"`
}
type ConversationReadResponse struct{}

type ProfileListRequest struct{}
type ProfileSummary struct {
	InUse          int           `json:"inUse"`
	Description    string        `json:"description"`
	Taste          int           `json:"taste"`
	Diagnostics    int           `json:"diagnostics"`
	Intelligence   int           `json:"intelligence"`
	Cost           int           `json:"cost"`
	Name           ProfileName   `json:"name"`
	Provider       ModelProvider `json:"provider"`
	Model          string        `json:"model"`
	Thinking       ThinkingLevel `json:"thinking"`
	AllowedModels  []string      `json:"allowedModels,omitempty"`
	CompactTokens  int           `json:"compactTokens,omitempty"`
	MaxConcurrent  int           `json:"maxConcurrent,omitempty"`
	MaxCallsPerRun int           `json:"maxCallsPerRun,omitempty"`
}
type ProfileListResponse struct {
	Profiles []ProfileSummary `json:"profiles"`
}

type RoomSetLimitsRequest struct {
	Room     RoomID `json:"room"`
	CPUs     int    `json:"cpus"`
	MemBytes int64  `json:"memBytes"`
}
type RoomSetLimitsResponse struct{}

type RoomPauseRequest struct {
	Room   RoomID `json:"room"`
	Paused bool   `json:"paused"`
}
type RoomPauseResponse struct{}
type SessionStopRequest struct {
	Session SessionID `json:"session"`
}
type SessionStopResponse struct {
	Stopping bool `json:"stopping"`
}

type RoomListRequest struct{}
type RoomSummary struct {
	ID       RoomID      `json:"id"`
	Name     string      `json:"name"`
	Paused   bool        `json:"paused"`
	Created  time.Time   `json:"created"`
	LastRead int64       `json:"lastRead"`
	Waiting  []OpID      `json:"waiting"`  // pending owner-class operations, which only the owner decides
	Working  []SessionID `json:"working"`  // open sessions with a running turn
	Failed   int         `json:"failed"`   // open sessions whose last turn that was not stopped failed, or whose box failed or is lost; each counts once
	Hostless int         `json:"hostless"` // open sessions whose box waits for a room host
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
type BoxReason string // The stored reason for the current box state, including admission waits and failures.

type SessionSummary struct {
	LastRead    int64         `json:"lastRead"`
	ID          SessionID     `json:"id"`
	Name        string        `json:"name"`
	Parent      SessionID     `json:"parent"`
	Profile     ProfileName   `json:"profile"`
	Box         BoxID         `json:"box"`
	BoxState    BoxState      `json:"boxState"`
	BoxReason   BoxReason     `json:"boxReason,omitempty"`
	Computer    ComputerState `json:"computer"`
	Goal        string        `json:"goal"`
	Turn        TurnID        `json:"turn,omitempty"`        // the running turn
	Activity    string        `json:"activity,omitempty"`    // the running turn's latest TurnActivity label
	LastOutcome TurnOutcome   `json:"lastOutcome,omitempty"` // of the last ended turn that was not stopped; a stopped turn neither fails nor succeeds
	ShellJobs   int           `json:"shellJobs,omitempty"`   // TurnEnded.ShellJobs of the last ended turn
	HadJobs     bool          `json:"hadJobs"`               // the session has ever run a background shell job, so a finished job stays reachable
	StartedAt   time.Time     `json:"startedAt"`             // when the session was created
	LastTurnAt  time.Time     `json:"lastTurnAt,omitzero"`   // when the latest turn ended; empty when no turn has ended
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
	UsageRateLimited   UsageCause = "rate_limited"
)

type AccountUsage struct {
	AccountID  AccountID     `json:"accountId,omitempty"`
	ObservedAt *time.Time    `json:"observedAt,omitempty"`
	Provider   string        `json:"provider"`
	Label      string        `json:"label"`
	Windows    []UsageWindow `json:"windows"`
	Cause      UsageCause    `json:"cause"`
}
type UsageWindowKind string

const (
	UsageWindowFiveHour UsageWindowKind = "five_hour"
	UsageWindowWeekly   UsageWindowKind = "weekly"
	UsageWindowOther    UsageWindowKind = "other"
)

type UsageWindow struct {
	Name        string          `json:"name"`
	Kind        UsageWindowKind `json:"kind"`
	Model       string          `json:"model,omitempty"`
	UsedPercent float64         `json:"usedPercent"`
	Seconds     int64           `json:"seconds"`
	ResetsAt    time.Time       `json:"resetsAt"`
}

type HostAddRequest struct {
	Rooms          []RoomID `json:"rooms,omitempty"`
	Name           HostName `json:"name"`
	SSH            string   `json:"ssh"`
	OwnerOnlyAdmin bool     `json:"ownerOnlyAdmin"`
}
type HostAddResponse struct {
	NoSwap bool `json:"noSwap"`
}

// HostListRequest asks for the hosts an owner terminal can open (5.15). Only the owner calls it.
type HostListRequest struct{}
type HostListResponse struct {
	Hosts []HostSummary `json:"hosts"` // ControlPlane first, then each added host by name
}

type BackupCopyFailure string

const (
	BackupCopyNoSpace BackupCopyFailure = "no_space"
	BackupCopyFailed  BackupCopyFailure = "failed"
)

type RoomShared struct {
	Room  RoomID `json:"room"`
	Bytes int64  `json:"bytes"`
}

type HostSummary struct {
	Shared            []RoomShared      `json:"shared"`
	Name              HostName          `json:"name"`
	Reachable         bool              `json:"reachable"`
	Boxes             int               `json:"boxes"`
	CPUs              int               `json:"cpus"`
	MemBytes          int64             `json:"memBytes"`
	CPUUsed           float64           `json:"cpuUsed"`
	MemUsed           int64             `json:"memUsed"`
	DiskBytes         int64             `json:"diskBytes"`
	DiskUsed          int64             `json:"diskUsed"`
	DiskLow           bool              `json:"diskLow"`
	BackupCopyEnabled bool              `json:"backupCopyEnabled,omitempty"`
	BackupCopyAt      time.Time         `json:"backupCopyAt,omitzero"`
	BackupCopyFailure BackupCopyFailure `json:"backupCopyFailure,omitempty"`
	LastSeen          time.Time         `json:"lastSeen,omitzero"`
	History           []HostLoadPoint   `json:"history"`
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
	ClientID string      `json:"clientId,omitempty"`
	Room     RoomID      `json:"room"`
	Name     string      `json:"name"`
	Parent   SessionID   `json:"parent,omitempty"`
	Repo     RepoName    `json:"repo,omitempty"`
	Profile  ProfileName `json:"profile"`
	Goal     string      `json:"goal,omitempty"`
	Brief    string      `json:"brief,omitempty"`
	Size     BoxSizeName `json:"size,omitempty"`
	SameHost bool        `json:"sameHost,omitempty"`
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
type SessionSetProfileRequest struct {
	Session SessionID   `json:"session"`
	Profile ProfileName `json:"profile"`
}
type SessionSetProfileResponse struct{}
type SessionRestartRequest struct {
	Session SessionID `json:"session"`
}
type SessionRestartResponse struct{}
type PostRequest struct {
	Room         RoomID       `json:"room"`
	Text         string       `json:"text"`
	Mentions     []string     `json:"mentions,omitempty"`
	Home         Conversation `json:"home,omitempty"`
	ReplyTo      EventID      `json:"replyTo,omitempty"`
	Attachments  []Attachment `json:"attachments,omitempty"`
	ClientID     string       `json:"clientId,omitempty"`     // at most 64 characters; a repeat from the same author in a room returns the first post
	ToolCallID   string       `json:"toolCallId,omitempty"`   // Pi transcript call that sent this post
	VisualUpdate EventID      `json:"visualUpdate,omitempty"` // original visual ID, for a new version
	VisualReply  EventID      `json:"visualReply,omitempty"`  // visual version this post answers
}
type PostResponse struct {
	Event EventID `json:"event"`
	Seq   int64   `json:"seq"`
}
type SessionSearchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}
type SearchSession struct {
	ID     SessionID `json:"id"`
	Name   string    `json:"name"`
	Room   RoomID    `json:"room"`
	Closed bool      `json:"closed"`
}
type SessionSearchResponse struct {
	Sessions []SearchSession `json:"sessions"`
	Total    int             `json:"total"`
}
type PostSearchRequest struct {
	Query  string `json:"query"`
	Room   RoomID `json:"room,omitempty"`
	Before int64  `json:"before,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}
type SearchPost struct {
	Seq    int64        `json:"seq"`
	Room   RoomID       `json:"room"`
	Home   Conversation `json:"home"`
	Author Principal    `json:"author"`
	Time   time.Time    `json:"time"`
	Text   string       `json:"text"`
}
type PostSearchResponse struct {
	Names map[SessionID]string `json:"names"`
	Posts []SearchPost         `json:"posts"`
	Total int                  `json:"total"`
}
type EventsListRequest struct {
	Event     EventID     `json:"event,omitempty"` // return this room event by ID
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
type BoxHelloRequest struct{}
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
	GitName       string           `json:"gitName,omitempty"`
	GitEmail      string           `json:"gitEmail,omitempty"`
	CompactTokens int              `json:"compactTokens,omitempty"`
	Instructions  string           `json:"instructions"`
	Session       SessionID        `json:"session"`
	Box           BoxID            `json:"box"`
	Model         ModelChoice      `json:"model"`
	Repos         []BoxRepo        `json:"repos,omitempty"`
	Transcripts   []TranscriptFile `json:"transcripts,omitempty"`
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
type SessionWorkRequest struct {
	Session SessionID `json:"session"`
	Before  TurnID    `json:"before,omitempty"`
	Limit   int       `json:"limit,omitempty"`
}
type SessionWorkTurn struct {
	Started Event   `json:"started"`
	Ended   *Event  `json:"ended,omitempty"`
	Inputs  []Event `json:"inputs"`
	Posts   []Event `json:"posts"`
}
type SessionWorkResponse struct {
	Turns []SessionWorkTurn `json:"turns"`
	More  bool              `json:"more"`
	Total int               `json:"total"`
}

type TranscriptReadRequest struct {
	Session        SessionID      `json:"session"`
	Turn           TurnID         `json:"turn,omitempty"`
	Before         TurnID         `json:"before,omitempty"`
	Limit          int            `json:"limit,omitempty"`
	Item           string         `json:"item,omitempty"`
	Part           TranscriptPart `json:"part,omitempty"`
	Offset         int            `json:"offset,omitempty"`
	AfterItem      string         `json:"afterItem,omitempty"`
	ItemIndex      int            `json:"itemIndex,omitempty"`
	OlderItems     bool           `json:"olderItems,omitempty"`
	MandatoryItems bool           `json:"mandatoryItems,omitempty"`
	MandatoryIndex int            `json:"mandatoryIndex,omitempty"`
	MandatoryPosts bool           `json:"mandatoryPosts,omitempty"`
	MandatoryPost  int            `json:"mandatoryPost,omitempty"`
	Tail           bool           `json:"tail,omitempty"`
	PostIndex      int            `json:"postIndex,omitempty"`
}
type TranscriptPart string

const (
	TranscriptPartOutput    TranscriptPart = "output"
	TranscriptPartText      TranscriptPart = "text"
	TranscriptPartArguments TranscriptPart = "arguments"
	TranscriptPartDiff      TranscriptPart = "diff"
)

type TranscriptItemKind string

const (
	TranscriptText     TranscriptItemKind = "text"
	TranscriptThinking TranscriptItemKind = "thinking"
	TranscriptTool     TranscriptItemKind = "tool"
	TranscriptPost     TranscriptItemKind = "post"
	TranscriptTooLarge TranscriptItemKind = "too_large"
)

type TranscriptItem struct {
	ID           string                  `json:"id"`
	Index        int                     `json:"index,omitempty"`
	Turn         TurnID                  `json:"turn"`
	Kind         TranscriptItemKind      `json:"kind"`
	At           time.Time               `json:"at"`
	Ended        *time.Time              `json:"ended,omitempty"`
	Text         string                  `json:"text,omitempty"`
	Tool         string                  `json:"tool,omitempty"`
	Arguments    string                  `json:"arguments,omitempty"`
	Output       string                  `json:"output,omitempty"`
	Diff         string                  `json:"diff,omitempty"`
	Failed       bool                    `json:"failed,omitempty"`
	More         bool                    `json:"more,omitempty"`
	Event        EventID                 `json:"event,omitempty"`
	Room         RoomID                  `json:"room,omitempty"`
	Seq          int64                   `json:"seq,omitempty"`
	Home         *Conversation           `json:"home,omitempty"`
	Bytes        int64                   `json:"bytes,omitempty"`
	Attachments  []Attachment            `json:"attachments,omitempty"`
	Posts        []TranscriptItem        `json:"posts,omitempty"`
	PostsOmitted int                     `json:"postsOmitted,omitempty"`
	PostsNext    *TranscriptPostLocation `json:"postsNext,omitempty"`
}

// Session-origin posts can only use a room feed; post_api rejects DM homes.
type TranscriptPostLocation struct {
	Room RoomID `json:"room"`
	Seq  int64  `json:"seq"`
}
type TranscriptInput struct {
	Home *Conversation `json:"home,omitempty"`
	At   time.Time     `json:"at"`
	ID   EventID       `json:"id"`
	Seq  int64         `json:"seq"`
	Room RoomID        `json:"room"`
	Type EventType     `json:"type"`
	From Principal     `json:"from"`
	Text string        `json:"text"`
	More bool          `json:"more,omitempty"`
}
type TranscriptTurn struct {
	ID                TurnID            `json:"id"`
	Started           time.Time         `json:"started"`
	Ended             *time.Time        `json:"ended,omitempty"`
	DurationMS        int64             `json:"durationMs,omitempty"`
	Outcome           TurnOutcome       `json:"outcome,omitempty"`
	Cause             FailureCause      `json:"cause,omitempty"`
	Inputs            []TranscriptInput `json:"inputs"`
	Items             []TranscriptItem  `json:"items"`
	More              bool              `json:"more"`
	After             string            `json:"after,omitempty"`
	NextItem          int               `json:"nextItem,omitempty"`
	OlderItems        bool              `json:"olderItems,omitempty"`
	MandatoryItems    bool              `json:"mandatoryItems,omitempty"`
	NextMandatory     int               `json:"nextMandatory,omitempty"`
	MandatoryPosts    bool              `json:"mandatoryPosts,omitempty"`
	NextMandatoryPost int               `json:"nextMandatoryPost,omitempty"`
	Tail              bool              `json:"tail,omitempty"`
	NextPost          int               `json:"nextPost,omitempty"`
}
type TranscriptStep struct {
	Turn    TurnID     `json:"turn"`
	Tool    string     `json:"tool"`
	Label   string     `json:"label"`
	Started time.Time  `json:"started"`
	Ended   *time.Time `json:"ended,omitempty"`
	Failed  bool       `json:"failed"`
	Preview string     `json:"preview"`
}
type TranscriptReadResponse struct {
	Steps      []TranscriptStep `json:"steps"`
	Truncated  bool             `json:"truncated"`
	Turns      []TranscriptTurn `json:"turns"`
	Before     TurnID           `json:"before,omitempty"`
	More       bool             `json:"more"`
	Chunk      string           `json:"chunk,omitempty"`
	NextOffset int              `json:"nextOffset,omitempty"`
}
type TranscriptAppendRequest struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	Bytes  []byte `json:"bytes"`
}
type TranscriptAppendResponse struct {
	AckedOffset int64 `json:"ackedOffset"`
}
type ShellJobEndKind string

const (
	ShellJobExited   ShellJobEndKind = "exit"
	ShellJobSignaled ShellJobEndKind = "signal"
	ShellJobStopped  ShellJobEndKind = "stopped"
)

type ShellJobEnd struct {
	Kind   ShellJobEndKind `json:"kind"`
	Code   int             `json:"code,omitempty"`
	Signal string          `json:"signal,omitempty"`
}

type ShellJobEndedRequest struct {
	Description string       `json:"description,omitempty"`
	End         *ShellJobEnd `json:"end,omitempty"`
	Job         ShellJobID   `json:"job"`
	ExitCode    int          `json:"exitCode"`
	Tail        string       `json:"tail"` // the last output, masked
}
type ShellJobEndedResponse struct{}
type TurnInput struct {
	Delivery DeliverySeq  `json:"delivery"`
	Surface  Surface      `json:"surface"`
	Reply    ReplyChannel `json:"reply"`
	Text     string       `json:"text"`
}
type TurnStart struct {
	Sections []ContextSection `json:"sections"`
	Turn     TurnID           `json:"turn"`
	Inputs   []TurnInput      `json:"inputs"`
	Model    ModelChoice      `json:"model"` // the session profile's model when the turn was dispatched
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
type ComputerWatchStart struct {
	Generation uint64 `json:"generation"`
}
type ComputerWatchStop struct{}

type ComputerInputKind string

const (
	InputKeyDown   ComputerInputKind = "key.down"
	InputKeyUp     ComputerInputKind = "key.up"
	InputChar      ComputerInputKind = "char" // Text is typed as is, for phone keyboards and input methods
	InputMouseMove ComputerInputKind = "mouse.move"
	InputMouseDown ComputerInputKind = "mouse.down"
	InputMouseUp   ComputerInputKind = "mouse.up"
	InputWheel     ComputerInputKind = "wheel"
)

type MouseButton string

const (
	ButtonLeft   MouseButton = "left"
	ButtonMiddle MouseButton = "middle"
	ButtonRight  MouseButton = "right"
)

// InputModifiers is the DevTools modifier mask of the keys held during an event.
type InputModifiers int

const (
	ModAlt   InputModifiers = 1
	ModCtrl  InputModifiers = 2
	ModMeta  InputModifiers = 4
	ModShift InputModifiers = 8
)

// FrameSize is the size in pixels of the screencast JPEG the owner saw when the event was made.
type FrameSize struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// ComputerInput is one owner event. Key events use Key (a DOM KeyboardEvent.key value such as "a" or
// "Enter"), Code and Modifiers. Mouse and wheel events use X and Y in frame pixels; mouse down and up
// also use Button, and a wheel event uses DeltaX and DeltaY in frame pixels.
type ComputerInput struct {
	Kind      ComputerInputKind `json:"kind"`
	Key       string            `json:"key,omitempty"`
	Code      string            `json:"code,omitempty"`
	Text      string            `json:"text,omitempty"`
	Modifiers InputModifiers    `json:"modifiers,omitempty"`
	Button    MouseButton       `json:"button,omitempty"`
	X         float64           `json:"x"`
	Y         float64           `json:"y"`
	DeltaX    float64           `json:"deltaX,omitempty"`
	DeltaY    float64           `json:"deltaY,omitempty"`
}

// ComputerInputRequest sends a batch of owner events, in order, to the session's computer. Frame is
// required, and every position in the batch is in its pixels.
type ComputerInputRequest struct {
	Session SessionID       `json:"session"`
	Frame   FrameSize       `json:"frame"`
	Events  []ComputerInput `json:"events"`
}
type ComputerInputResponse struct{}

// ComputerInputBatch is what the engine sends the box agent for a ComputerInputRequest.
type ComputerInputBatch struct {
	Frame  FrameSize       `json:"frame"`
	Events []ComputerInput `json:"events"`
}

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
	Description string       `json:"description,omitempty"`
	End         *ShellJobEnd `json:"end,omitempty"`
	Job         ShellJobID   `json:"job"`
	Command     string       `json:"command"`
	Running     bool         `json:"running"`
	ExitCode    int          `json:"exitCode"` // when Running is false
	Text        string       `json:"text"`
	Reset       bool         `json:"reset,omitempty"`
	Started     time.Time    `json:"started"`        // by the box's clock
	Ended       time.Time    `json:"ended,omitzero"` // by the box's clock, once the job has ended
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

// ErrUnknownBoxMessage wraps the decode error for a message type this build does not know, so an older box
// can skip a message from a newer engine.
var ErrUnknownBoxMessage = errors.New("unknown box message type")

type BoxMessage struct {
	Body BoxMessageBody `json:"body"`
}

func (ComputerWatchStart) BoxMessageType() BoxMessageType { return "computer.watch.start" }
func (ComputerWatchStart) isBoxMessageBody()              {}
func (ComputerWatchStop) BoxMessageType() BoxMessageType  { return "computer.watch.stop" }
func (ComputerWatchStop) isBoxMessageBody()               {}
func (ComputerInputBatch) BoxMessageType() BoxMessageType { return "computer.input" }
func (ComputerInputBatch) isBoxMessageBody()              {}
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

type TranscriptFetchRequest struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
}
type TranscriptFetchResponse struct {
	Bytes []byte `json:"bytes"`
}

type TranscriptFile struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}
