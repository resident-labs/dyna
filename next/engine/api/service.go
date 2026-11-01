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
	WebSearch(context.Context, contract.Principal, contract.WebSearchRequest) (contract.WebSearchResponse, *contract.Error)
	WebsearchSetKey(context.Context, contract.Principal, contract.WebsearchSetKeyRequest) (contract.WebsearchSetKeyResponse, *contract.Error)
	WebsearchRemoveKey(context.Context, contract.Principal, contract.WebsearchRemoveKeyRequest) (contract.WebsearchRemoveKeyResponse, *contract.Error)
	ContextSnapshot(context.Context, contract.Principal, contract.ContextSnapshotRequest) (contract.ContextSnapshotResponse, *contract.Error)
	GoalSet(context.Context, contract.Principal, contract.GoalSetRequest) (contract.GoalSetResponse, *contract.Error)
	GoalList(context.Context, contract.Principal, contract.GoalListRequest) (contract.GoalListResponse, *contract.Error)
	GoalItem(context.Context, contract.Principal, contract.GoalItemRequest) (contract.GoalItemResponse, *contract.Error)
	GoalDone(context.Context, contract.Principal, contract.GoalDoneRequest) (contract.GoalDoneResponse, *contract.Error)
	GoalJournal(context.Context, contract.Principal, contract.GoalJournalRequest) (contract.GoalJournalResponse, *contract.Error)
	DevicePair(context.Context, contract.Principal, contract.DevicePairRequest) (contract.DevicePairResponse, *contract.Error)
	DeviceList(context.Context, contract.Principal, contract.DeviceListRequest) (contract.DeviceListResponse, *contract.Error)
	DeviceRevoke(context.Context, contract.Principal, contract.DeviceRevokeRequest) (contract.DeviceRevokeResponse, *contract.Error)
	DeviceSetScopes(context.Context, contract.Principal, contract.DeviceSetScopesRequest) (contract.DeviceSetScopesResponse, *contract.Error)
	InstructionsGet(context.Context, contract.Principal, contract.InstructionsGetRequest) (contract.InstructionsGetResponse, *contract.Error)
	InstructionsSet(context.Context, contract.Principal, contract.InstructionsSetRequest) (contract.InstructionsSetResponse, *contract.Error)
	OperationScope(contract.OpKind) contract.Scope
	Terminal(context.Context, contract.SessionID, contract.HostName) ([]string, func(), *contract.Error)
	ShellWatch(context.Context, contract.SessionID) (<-chan contract.ShellJobOutput, *contract.Error)
	ShellOutput(context.Context, contract.Principal, contract.ShellOutputRequest) (contract.ShellOutputResponse, *contract.Error)
	ComputerChanged(context.Context, contract.Principal, contract.ComputerChangedRequest) (contract.ComputerChangedResponse, *contract.Error)
	ComputerWatch(context.Context, contract.SessionID) (<-chan []byte, *contract.Error)
	ComputerInput(context.Context, contract.Principal, contract.ComputerInputRequest) (contract.ComputerInputResponse, *contract.Error)
	ComputerFrames(context.Context, contract.Principal, uint64, io.Reader) *contract.Error
	RoomSetLimits(context.Context, contract.Principal, contract.RoomSetLimitsRequest) (contract.RoomSetLimitsResponse, *contract.Error)
	RoomPause(context.Context, contract.Principal, contract.RoomPauseRequest) (contract.RoomPauseResponse, *contract.Error)
	SessionStop(context.Context, contract.Principal, contract.SessionStopRequest) (contract.SessionStopResponse, *contract.Error)
	RoomList(context.Context, contract.Principal, contract.RoomListRequest) (contract.RoomListResponse, *contract.Error)
	SessionList(context.Context, contract.Principal, contract.SessionListRequest) (contract.SessionListResponse, *contract.Error)
	HostexecRun(context.Context, contract.Principal, contract.HostexecRunRequest) (contract.HostexecRunResponse, *contract.Error)
	HostexecList(context.Context, contract.Principal, contract.HostexecListRequest) (contract.HostexecListResponse, *contract.Error)
	OpSubmit(context.Context, contract.Principal, contract.OpSubmitRequest) (contract.OpSubmitResponse, *contract.Error)
	OpDecide(context.Context, contract.Principal, contract.OpDecideRequest) (contract.OpDecideResponse, *contract.Error)
	OpWithdraw(context.Context, contract.Principal, contract.OpWithdrawRequest) (contract.OpWithdrawResponse, *contract.Error)
	OpList(context.Context, contract.Principal, contract.OpListRequest) (contract.OpListResponse, *contract.Error)
	QuestionAsk(context.Context, contract.Principal, contract.QuestionAskRequest) (contract.QuestionAskResponse, *contract.Error)
	QuestionAnswer(context.Context, contract.Principal, contract.QuestionAnswerRequest) (contract.QuestionAnswerResponse, *contract.Error)
	QuestionList(context.Context, contract.Principal, contract.QuestionListRequest) (contract.QuestionListResponse, *contract.Error)
	HostAdd(context.Context, contract.Principal, contract.HostAddRequest) (contract.HostAddResponse, *contract.Error)
	ProfileList(context.Context, contract.Principal, contract.ProfileListRequest) (contract.ProfileListResponse, *contract.Error)
	ProfileSet(context.Context, contract.Principal, contract.ProfileSetRequest) (contract.ProfileSetResponse, *contract.Error)
	ProfileRemove(context.Context, contract.Principal, contract.ProfileRemoveRequest) (contract.ProfileRemoveResponse, *contract.Error)
	HostList(context.Context, contract.Principal, contract.HostListRequest) (contract.HostListResponse, *contract.Error)
	AccountsSetLimits(context.Context, contract.Principal, contract.AccountsSetLimitsRequest) (contract.AccountsSetLimitsResponse, *contract.Error)
	AccountsRemove(context.Context, contract.Principal, contract.AccountsRemoveRequest) (contract.AccountsRemoveResponse, *contract.Error)
	AccountsRename(context.Context, contract.Principal, contract.AccountsRenameRequest) (contract.AccountsRenameResponse, *contract.Error)
	AccountsList(context.Context, contract.Principal, contract.AccountsListRequest) (contract.AccountsListResponse, *contract.Error)
	AccountsConnect(context.Context, contract.Principal, contract.AccountsConnectRequest) (contract.AccountsConnectResponse, *contract.Error)
	AccountsReconnect(context.Context, contract.Principal, contract.AccountsReconnectRequest) (contract.AccountsReconnectResponse, *contract.Error)
	AccountsConnectCallback(context.Context, contract.Principal, contract.AccountsConnectCallbackRequest) (contract.AccountsConnectCallbackResponse, *contract.Error)
	AccountsConnectStatus(context.Context, contract.Principal, contract.AccountsConnectStatusRequest) (contract.AccountsConnectStatusResponse, *contract.Error)
	AccountsImportOAuth(context.Context, contract.Principal, contract.AccountsImportOAuthRequest) (contract.AccountsImportOAuthResponse, *contract.Error)
	AccountsSetKey(context.Context, contract.Principal, contract.AccountsSetKeyRequest) (contract.AccountsSetKeyResponse, *contract.Error)
	AccountsQuota(context.Context, contract.Principal, contract.AccountsQuotaRequest) (contract.AccountsQuotaResponse, *contract.Error)
	ProxyKey(context.Context, contract.Principal, contract.ProxyKeyRequest) (contract.ProxyKeyResponse, *contract.Error)
	ProxyUsage(context.Context, contract.Principal, contract.ProxyUsageRequest) (contract.ProxyUsageResponse, *contract.Error)
	RoomCreate(context.Context, contract.Principal, contract.RoomCreateRequest) (contract.RoomCreateResponse, *contract.Error)
	RoomDelete(context.Context, contract.Principal, contract.RoomDeleteRequest) (contract.RoomDeleteResponse, *contract.Error)
	HostSetLimits(context.Context, contract.Principal, contract.HostSetLimitsRequest) (contract.HostSetLimitsResponse, *contract.Error)
	RoomRepoAdd(context.Context, contract.Principal, contract.RoomRepoAddRequest) (contract.RoomRepoAddResponse, *contract.Error)
	RoomRepos(context.Context, contract.Principal, contract.RoomReposRequest) (contract.RoomReposResponse, *contract.Error)
	RepoCISet(context.Context, contract.Principal, contract.RepoCISetRequest) (contract.RepoCISetResponse, *contract.Error)
	SessionStart(context.Context, contract.Principal, contract.SessionStartRequest) (contract.SessionStartResponse, *contract.Error)
	ComposeList(context.Context, contract.Principal, contract.ComposeListRequest) (contract.ComposeListResponse, *contract.Error)
	SessionMove(context.Context, contract.Principal, contract.SessionMoveRequest) (contract.SessionMoveResponse, *contract.Error)
	SessionClose(context.Context, contract.Principal, contract.SessionCloseRequest) (contract.SessionCloseResponse, *contract.Error)
	SessionSetProfile(context.Context, contract.Principal, contract.SessionSetProfileRequest) (contract.SessionSetProfileResponse, *contract.Error)
	SessionAccount(context.Context, contract.Principal, contract.SessionAccountRequest) (contract.SessionAccountResponse, *contract.Error)
	SessionSetAccount(context.Context, contract.Principal, contract.SessionSetAccountRequest) (contract.SessionSetAccountResponse, *contract.Error)
	SessionRestart(context.Context, contract.Principal, contract.SessionRestartRequest) (contract.SessionRestartResponse, *contract.Error)
	SessionSearch(context.Context, contract.Principal, contract.SessionSearchRequest) (contract.SessionSearchResponse, *contract.Error)
	PostSearch(context.Context, contract.Principal, contract.PostSearchRequest) (contract.PostSearchResponse, *contract.Error)
	PrList(context.Context, contract.Principal, contract.PrListRequest) (contract.PrListResponse, *contract.Error)
	PrStatus(context.Context, contract.Principal, contract.PrStatusRequest) (contract.PrStatusResponse, *contract.Error)
	PrTrack(context.Context, contract.Principal, contract.PrTrackRequest) (contract.PrTrackResponse, *contract.Error)
	PrUntrack(context.Context, contract.Principal, contract.PrUntrackRequest) (contract.PrUntrackResponse, *contract.Error)
	IssueList(context.Context, contract.Principal, contract.IssueListRequest) (contract.IssueListResponse, *contract.Error)
	IssueStatus(context.Context, contract.Principal, contract.IssueStatusRequest) (contract.IssueStatusResponse, *contract.Error)
	IssueTrack(context.Context, contract.Principal, contract.IssueTrackRequest) (contract.IssueTrackResponse, *contract.Error)
	IssueUntrack(context.Context, contract.Principal, contract.IssueUntrackRequest) (contract.IssueUntrackResponse, *contract.Error)
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
	SessionWork(context.Context, contract.Principal, contract.SessionWorkRequest) (contract.SessionWorkResponse, *contract.Error)
	ShellJobEnded(context.Context, contract.Principal, contract.ShellJobEndedRequest) (contract.ShellJobEndedResponse, *contract.Error)
	WorkflowRun(context.Context, contract.Principal, contract.WorkflowRunRequest) (contract.WorkflowRunResponse, *contract.Error)
	WorkflowStatus(context.Context, contract.Principal, contract.WorkflowStatusRequest) (contract.WorkflowStatusResponse, *contract.Error)
	WorkflowList(context.Context, contract.Principal, contract.WorkflowListRequest) (contract.WorkflowListResponse, *contract.Error)
	WorkflowPin(context.Context, contract.Principal, contract.WorkflowPinRequest) (contract.WorkflowPinResponse, *contract.Error)
	WorkflowStepStart(context.Context, contract.Principal, contract.WorkflowStepStartRequest) (contract.WorkflowStepStartResponse, *contract.Error)
	WorkflowStepEnd(context.Context, contract.Principal, contract.WorkflowStepEndRequest) (contract.WorkflowStepEndResponse, *contract.Error)
	WorkflowStepTranscript(context.Context, contract.Principal, contract.WorkflowStepTranscriptRequest) (contract.WorkflowStepTranscriptResponse, *contract.Error)
	SubscriptionAdd(context.Context, contract.Principal, contract.SubscriptionAddRequest) (contract.SubscriptionAddResponse, *contract.Error)
	SubscriptionList(context.Context, contract.Principal, contract.SubscriptionListRequest) (contract.SubscriptionListResponse, *contract.Error)
	SubscriptionRemove(context.Context, contract.Principal, contract.SubscriptionRemoveRequest) (contract.SubscriptionRemoveResponse, *contract.Error)
	SubscriptionSetEnabled(context.Context, contract.Principal, contract.SubscriptionSetEnabledRequest) (contract.SubscriptionSetEnabledResponse, *contract.Error)
	// HookPost posts for a Run. Its socket fixes the subscription and the room.
	HookPost(context.Context, contract.SubscriptionID, contract.RoomID, contract.PostRequest) (contract.PostResponse, *contract.Error)
	// HookTrack tracks a PR for a Run, for the session it names in its room.
	HookTrack(context.Context, contract.RoomID, string, contract.PrTrackRequest) (contract.PrTrackResponse, *contract.Error)
	// HookTrackIssue tracks an issue for a Run, for the session it names in its room.
	HookTrackIssue(context.Context, contract.RoomID, string, contract.IssueTrackRequest) (contract.IssueTrackResponse, *contract.Error)
	// Events atomically subscribes after seq, including committed events not yet
	// delivered. The producer closes the channel when ctx ends.
	Events(context.Context, contract.Principal, contract.RoomID, int64) (<-chan contract.Event, *contract.Error)
	// BoxChannel subscribes atomically and first emits the current durable
	// delivery and turn state. Reconnects repeat unfinished work.
	BoxChannel(context.Context, contract.Principal) (<-chan contract.BoxMessage, *contract.Error)
}
