package api

import (
	"context"
	"github.com/resident-labs/dyna/contract"
)

// Caller is fixed by the listener, not by a request body.
type Caller struct {
	Principal contract.Principal
	Box       contract.BoxID
	Room      contract.RoomID
}

// Service implements the engine operations. It must authorize resource targets
// against the caller as well as applying the API's listener-level policy.
type Service interface {
	RoomCreate(context.Context, Caller, contract.RoomCreateRequest) (contract.RoomCreateResponse, *contract.Error)
	SessionStart(context.Context, Caller, contract.SessionStartRequest) (contract.SessionStartResponse, *contract.Error)
	SessionClose(context.Context, Caller, contract.SessionCloseRequest) (contract.SessionCloseResponse, *contract.Error)
	Post(context.Context, Caller, contract.PostRequest) (contract.PostResponse, *contract.Error)
	EventsList(context.Context, Caller, contract.EventsListRequest) (contract.EventsListResponse, *contract.Error)
	BoxHello(context.Context, Caller, contract.BoxHelloRequest) (contract.BoxHelloResponse, *contract.Error)
	TurnOutput(context.Context, Caller, contract.TurnOutputRequest) (contract.TurnOutputResponse, *contract.Error)
	TranscriptAppend(context.Context, Caller, contract.TranscriptAppendRequest) (contract.TranscriptAppendResponse, *contract.Error)
	DeliveryDone(context.Context, Caller, contract.DeliveryDoneRequest) (contract.DeliveryDoneResponse, *contract.Error)
	// Events atomically subscribes after seq, including committed events not yet
	// delivered. The producer closes the channel when ctx ends.
	Events(context.Context, Caller, contract.RoomID, int64) (<-chan contract.Event, *contract.Error)
	// BoxChannel subscribes atomically and first emits the current durable
	// delivery and turn state. Reconnects repeat unfinished work.
	BoxChannel(context.Context, Caller) (<-chan contract.BoxMessage, *contract.Error)
}
