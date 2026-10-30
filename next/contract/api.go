package contract

type Method string

const (
	MethodArchiveDone      Method = "archive.done"
	MethodDeliveryDone     Method = "delivery.done"
	MethodHostAdd          Method = "host.add"
	MethodRoomCreate       Method = "room.create"
	MethodRoomRepoAdd      Method = "room.repo.add"
	MethodRoomRepos        Method = "room.repos"
	MethodSessionStart     Method = "session.start"
	MethodSessionClose     Method = "session.close"
	MethodPost             Method = "post"
	MethodEventsList       Method = "events.list"
	MethodBoxHello         Method = "box.hello"
	MethodTurnOutput       Method = "turn.output"
	MethodTranscriptAppend Method = "transcript.append"
)

type ErrorCode string

const (
	ErrInvalid       ErrorCode = "invalid"
	ErrNotFound      ErrorCode = "not_found"
	ErrForbidden     ErrorCode = "forbidden"
	ErrConflict      ErrorCode = "conflict"
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

type HostAddRequest struct {
	Name           HostName `json:"name"`
	SSH            string   `json:"ssh"`
	OwnerOnlyAdmin bool     `json:"ownerOnlyAdmin"`
}
type HostAddResponse struct{}

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
type SessionCloseRequest struct {
	Session SessionID `json:"session"`
	Discard bool      `json:"discard,omitempty"`
}
type SessionCloseResponse struct{}
type PostRequest struct {
	Room     RoomID       `json:"room"`
	Text     string       `json:"text"`
	Mentions []string     `json:"mentions,omitempty"`
	Home     Conversation `json:"home,omitempty"`
	ReplyTo  EventID      `json:"replyTo,omitempty"`
}
type PostResponse struct {
	Event EventID `json:"event"`
	Seq   int64   `json:"seq"`
}
type EventsListRequest struct {
	Room     RoomID      `json:"room"`
	AfterSeq int64       `json:"afterSeq"`
	Limit    int         `json:"limit"`
	Types    []EventType `json:"types,omitempty"`
}
type EventsListResponse struct {
	Events []Event `json:"events"`
	Next   int64   `json:"next"`
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
	Upgrade string      `json:"upgrade,omitempty"`
}
type TurnOutputRequest struct {
	Error   string      `json:"error,omitempty"`
	Turn    TurnID      `json:"turn"`
	Outcome TurnOutcome `json:"outcome"`
	Final   string      `json:"final,omitempty"`
	Model   string      `json:"model"`
	Usage   Usage       `json:"usage"`
}
type TurnOutputResponse struct{}
type TranscriptAppendRequest struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	Bytes  []byte `json:"bytes"`
}
type TranscriptAppendResponse struct {
	AckedOffset int64 `json:"ackedOffset"`
}
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
