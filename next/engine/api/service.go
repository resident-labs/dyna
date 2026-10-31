package api

import (
	"context"
	"github.com/resident-labs/dyna/contract"
	"io"
)

// Caller is fixed by the listener, not by a request body.
type Caller struct {
	Principal contract.Principal
	Box       contract.BoxID
	Room      contract.RoomID
	Device    string
	Scopes    []contract.Scope
	Web       bool
}

// Service implements the engine operations. It must authorize resource targets
// against the caller as well as applying the API's listener-level policy.
type Service interface {
	OperationScope(contract.OpKind) contract.Scope
	Terminal(context.Context, contract.SessionID, contract.HostName) ([]string, func(), *contract.Error)
	ShellWatch(context.Context, contract.SessionID) (<-chan contract.ShellJobOutput, *contract.Error)
	ShellOutput(context.Context, contract.Principal, contract.ShellOutputRequest) (contract.ShellOutputResponse, *contract.Error)
	ComputerChanged(context.Context, contract.Principal, contract.ComputerChangedRequest) (contract.ComputerChangedResponse, *contract.Error)
	ComputerWatch(context.Context, contract.SessionID) (<-chan []byte, *contract.Error)
	ComputerFrames(context.Context, contract.Principal, uint64, io.Reader) *contract.Error
	RuleAdd(context.Context, contract.Principal, contract.RuleAddRequest) (contract.RuleAddResponse, *contract.Error)
	RuleRemove(context.Context, contract.Principal, contract.RuleRemoveRequest) (contract.RuleRemoveResponse, *contract.Error)
	RuleEdit(context.Context, contract.Principal, contract.RuleEditRequest) (contract.RuleEditResponse, *contract.Error)
	RuleList(context.Context, contract.Principal, contract.RuleListRequest) (contract.RuleListResponse, *contract.Error)
	RoomPause(context.Context, contract.Principal, contract.RoomPauseRequest) (contract.RoomPauseResponse, *contract.Error)
	SessionStop(context.Context, contract.Principal, contract.SessionStopRequest) (contract.SessionStopResponse, *contract.Error)
	RoomList(context.Context, contract.Principal, contract.RoomListRequest) (contract.RoomListResponse, *contract.Error)
	SessionList(context.Context, contract.Principal, contract.SessionListRequest) (contract.SessionListResponse, *contract.Error)
	HostexecRun(context.Context, contract.Principal, contract.HostexecRunRequest) (contract.HostexecRunResponse, *contract.Error)
	HostexecRevoke(context.Context, contract.Principal, contract.HostexecRevokeRequest) (contract.HostexecRevokeResponse, *contract.Error)
	OpSubmit(context.Context, contract.Principal, contract.OpSubmitRequest) (contract.OpSubmitResponse, *contract.Error)
	OpDecide(context.Context, contract.Principal, contract.OpDecideRequest) (contract.OpDecideResponse, *contract.Error)
	OpWithdraw(context.Context, contract.Principal, contract.OpWithdrawRequest) (contract.OpWithdrawResponse, *contract.Error)
	OpList(context.Context, contract.Principal, contract.OpListRequest) (contract.OpListResponse, *contract.Error)
	HostAdd(context.Context, contract.Principal, contract.HostAddRequest) (contract.HostAddResponse, *contract.Error)
	ProfileList(context.Context, contract.Principal, contract.ProfileListRequest) (contract.ProfileListResponse, *contract.Error)
	HostList(context.Context, contract.Principal, contract.HostListRequest) (contract.HostListResponse, *contract.Error)
	AccountsQuota(context.Context, contract.Principal, contract.AccountsQuotaRequest) (contract.AccountsQuotaResponse, *contract.Error)
	RoomCreate(context.Context, contract.Principal, contract.RoomCreateRequest) (contract.RoomCreateResponse, *contract.Error)
	RoomRepoAdd(context.Context, contract.Principal, contract.RoomRepoAddRequest) (contract.RoomRepoAddResponse, *contract.Error)
	RoomRepos(context.Context, contract.Principal, contract.RoomReposRequest) (contract.RoomReposResponse, *contract.Error)
	SessionStart(context.Context, contract.Principal, contract.SessionStartRequest) (contract.SessionStartResponse, *contract.Error)
	SessionMove(context.Context, contract.Principal, contract.SessionMoveRequest) (contract.SessionMoveResponse, *contract.Error)
	SessionClose(context.Context, contract.Principal, contract.SessionCloseRequest) (contract.SessionCloseResponse, *contract.Error)
	SessionSetProfile(context.Context, contract.Principal, contract.SessionSetProfileRequest) (contract.SessionSetProfileResponse, *contract.Error)
	SessionRestart(context.Context, contract.Principal, contract.SessionRestartRequest) (contract.SessionRestartResponse, *contract.Error)
	Post(context.Context, contract.Principal, contract.PostRequest) (contract.PostResponse, *contract.Error)
	EventsList(context.Context, contract.Principal, contract.EventsListRequest) (contract.EventsListResponse, *contract.Error)
	BoxHello(context.Context, contract.Principal, contract.BoxHelloRequest) (contract.BoxHelloResponse, *contract.Error)
	ConversationRead(context.Context, contract.Principal, contract.ConversationReadRequest) (contract.ConversationReadResponse, *contract.Error)
	TurnActivity(context.Context, contract.Principal, contract.TurnActivityRequest) (contract.TurnActivityResponse, *contract.Error)
	TurnOutput(context.Context, contract.Principal, contract.TurnOutputRequest) (contract.TurnOutputResponse, *contract.Error)
	DeliveryDone(context.Context, contract.Principal, contract.DeliveryDoneRequest) (contract.DeliveryDoneResponse, *contract.Error)
	ArchiveDone(context.Context, contract.Principal, contract.ArchiveDoneRequest) (contract.ArchiveDoneResponse, *contract.Error)
	TranscriptFetch(context.Context, contract.Principal, contract.TranscriptFetchRequest) (contract.TranscriptFetchResponse, *contract.Error)
	TranscriptAppend(context.Context, contract.Principal, contract.TranscriptAppendRequest) (contract.TranscriptAppendResponse, *contract.Error)
	TranscriptRead(context.Context, contract.Principal, contract.TranscriptReadRequest) (contract.TranscriptReadResponse, *contract.Error)
	ShellJobEnded(context.Context, contract.Principal, contract.ShellJobEndedRequest) (contract.ShellJobEndedResponse, *contract.Error)
	// Events atomically subscribes after seq, including committed events not yet
	// delivered. The producer closes the channel when ctx ends.
	Events(context.Context, contract.Principal, contract.RoomID, int64) (<-chan contract.Event, *contract.Error)
	// BoxChannel subscribes atomically and first emits the current durable
	// delivery and turn state. Reconnects repeat unfinished work.
	BoxChannel(context.Context, contract.Principal) (<-chan contract.BoxMessage, *contract.Error)
}
