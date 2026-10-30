package contract

type Method string

const (
	MethodRoomCreate       Method = "room.create"
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
	ErrInvalid     ErrorCode = "invalid"
	ErrNotFound    ErrorCode = "not_found"
	ErrForbidden   ErrorCode = "forbidden"
	ErrConflict    ErrorCode = "conflict"
	ErrStale       ErrorCode = "stale"
	ErrUnavailable ErrorCode = "unavailable"
	ErrStoreFromV1 ErrorCode = "store_from_v1"
	ErrStoreTooOld ErrorCode = "store_too_old"
)

type Error struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

type RoomCreateRequest struct {
	Name string `json:"name"`
}
type RoomCreateResponse struct {
	Room RoomID `json:"room"`
}
type SessionStartRequest struct {
	Room    RoomID      `json:"room"`
	Name    string      `json:"name"`
	Parent  SessionID   `json:"parent,omitempty"`
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
}
type SessionCloseResponse struct{}
type PostRequest struct {
	Room     RoomID   `json:"room"`
	Text     string   `json:"text"`
	Mentions []string `json:"mentions,omitempty"`
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
type BoxHelloResponse struct {
	Session SessionID `json:"session"`
	Box     BoxID     `json:"box"`
	Upgrade string    `json:"upgrade,omitempty"`
}
type TurnOutputRequest struct {
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

type DeliverySeq int64
type DeliveryDoneRequest struct {
	Delivery DeliverySeq `json:"delivery"`
}
type DeliveryDoneResponse struct{}
