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
	OpSubmit(context.Context, contract.Principal, contract.OpSubmitRequest) (contract.OpSubmitResponse, *contract.Error)
	OpDecide(context.Context, contract.Principal, contract.OpDecideRequest) (contract.OpDecideResponse, *contract.Error)
	OpWithdraw(context.Context, contract.Principal, contract.OpWithdrawRequest) (contract.OpWithdrawResponse, *contract.Error)
	OpList(context.Context, contract.Principal, contract.OpListRequest) (contract.OpListResponse, *contract.Error)
	HostAdd(context.Context, contract.Principal, contract.HostAddRequest) (contract.HostAddResponse, *contract.Error)
	RoomCreate(context.Context, contract.Principal, contract.RoomCreateRequest) (contract.RoomCreateResponse, *contract.Error)
	RoomRepoAdd(context.Context, contract.Principal, contract.RoomRepoAddRequest) (contract.RoomRepoAddResponse, *contract.Error)
	RoomRepos(context.Context, contract.Principal, contract.RoomReposRequest) (contract.RoomReposResponse, *contract.Error)
	SessionStart(context.Context, contract.Principal, contract.SessionStartRequest) (contract.SessionStartResponse, *contract.Error)
	SessionClose(context.Context, contract.Principal, contract.SessionCloseRequest) (contract.SessionCloseResponse, *contract.Error)
	Post(context.Context, contract.Principal, contract.PostRequest) (contract.PostResponse, *contract.Error)
	EventsList(context.Context, contract.Principal, contract.EventsListRequest) (contract.EventsListResponse, *contract.Error)
	BoxHello(context.Context, contract.Principal, contract.BoxHelloRequest) (contract.BoxHelloResponse, *contract.Error)
	TurnOutput(context.Context, contract.Principal, contract.TurnOutputRequest) (contract.TurnOutputResponse, *contract.Error)
	DeliveryDone(context.Context, contract.Principal, contract.DeliveryDoneRequest) (contract.DeliveryDoneResponse, *contract.Error)
	ArchiveDone(context.Context, contract.Principal, contract.ArchiveDoneRequest) (contract.ArchiveDoneResponse, *contract.Error)
	// Events atomically subscribes after seq, including committed events not yet
	// delivered. The producer closes the channel when ctx ends.
	Events(context.Context, contract.Principal, contract.RoomID, int64) (<-chan contract.Event, *contract.Error)
	// BoxChannel subscribes atomically and first emits the current durable
	// delivery and turn state. Reconnects repeat unfinished work.
	BoxChannel(context.Context, contract.Principal) (<-chan contract.BoxMessage, *contract.Error)
}
