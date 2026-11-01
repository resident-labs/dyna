package tools

import "github.com/resident-labs/dyna/contract"

func method(m contract.Method, scope contract.Scope, access Access, actions ...Action) MethodEntry {
	return MethodEntry{Method: m, Scope: scope, Access: access, Actions: actions}
}

// methodTable is the one method table, in method order. Rows without
// actions are engine-only or not yet served.
var methodTable = []MethodEntry{
	method(contract.MethodAccountsConnect, contract.ScopeAdmin, OwnerAccess),
	method(contract.MethodAccountsConnectCallback, contract.ScopeAdmin, OwnerAccess),
	method(contract.MethodAccountsConnectStatus, contract.ScopeAdmin, OwnerAccess),
	method(contract.MethodAccountsList, contract.ScopeRead, OwnerAccess),
	method(contract.MethodAccountsQuota, contract.ScopeRead, OwnerAccess),
	method(contract.MethodAccountsReconnect, contract.ScopeAdmin, OwnerAccess),
	method(contract.MethodAccountsRemove, contract.ScopeAdmin, OwnerAccess),
	method(contract.MethodAccountsSetKey, contract.ScopeAdmin, OwnerAccess),
	method(contract.MethodAccountsImportOAuth, contract.ScopeAdmin, OwnerAccess),
	method(contract.MethodAccountsSetLimits, contract.ScopeAdmin, OwnerAccess),
	method(contract.MethodArchiveDone, contract.ScopeRead, BoxAccess),
	method(contract.MethodBoxHello, contract.ScopeRead, BoxAccess),
	method(contract.MethodComputerChanged, contract.ScopeRead, BoxAccess),
	method(contract.MethodComputerInput, contract.ScopeComputers, OwnerAccess),
	method(contract.MethodConnectCancel, contract.ScopeAdmin, OwnerAccess),
	method(contract.MethodConnectStart, contract.ScopeAdmin, OwnerAccess),
	method(contract.MethodConnectStatus, contract.ScopeAdmin, OwnerAccess),
	method(contract.MethodConnectUnlink, contract.ScopeAdmin, OwnerAccess),
	method(contract.MethodContextSnapshot, contract.ScopeRead, UserAccess, act("context", "show", typ[contract.ContextSnapshotRequest](), "Show one context section in full.", "context show")),
	method(contract.MethodConversationRead, contract.ScopeOperate, OwnerAccess),
	method(contract.MethodDeliveryDone, contract.ScopeRead, BoxAccess),
	method(contract.MethodDeviceList, contract.ScopeAdmin, OwnerAccess,
		act("device", "list", typ[contract.DeviceListRequest](), "List devices", "device list"),
	),
	method(contract.MethodDevicePair, contract.ScopeAdmin, OwnerAccess,
		act("device", "pair", typ[contract.DevicePairRequest](), "Pair a device", "device pair"),
	),
	method(contract.MethodDeviceRevoke, contract.ScopeAdmin, OwnerAccess,
		act("device", "revoke", typ[contract.DeviceRevokeRequest](), "Revoke a device", "device revoke"),
	),
	method(contract.MethodDeviceScopes, "", OwnerAccess),
	method(contract.MethodDeviceSetScopes, contract.ScopeAdmin, OwnerAccess,
		act("device", "set_scopes", typ[contract.DeviceSetScopesRequest](), "Set device scopes", "device set-scopes"),
	),
	method(contract.MethodEventsList, contract.ScopeRead, UserAccess,
		act("room", "log", typ[contract.EventsListRequest](), "Read events in room. afterSeq and beforeSeq bound the page; limit, types and home filter it.", "room log"),
	),
	method(contract.MethodHostAdd, contract.ScopeAdmin, OwnerAccess,
		act("host", "add", typ[contract.HostAddRequest](), "Add a host", "host add"),
	),
	method(contract.MethodHostList, contract.ScopeRead, OwnerAccess),
	method(contract.MethodHostexecRevoke, contract.ScopeAdmin, OwnerAccess,
		act("hostexec", "revoke", typ[contract.HostexecRevokeRequest](), "Revoke a host grant", "hostexec revoke"),
	),
	method(contract.MethodHostexecRun, contract.ScopeRead, BoxAccess,
		act("hostexec", "run", typ[contract.HostexecRunRequest](), "Run argv on host with a grant. cwd and timeout are optional.", "hostexec run"),
	),
	method(contract.MethodInstructionsGet, contract.ScopeRead, OwnerAccess),
	method(contract.MethodInstructionsSet, contract.ScopeAdmin, OwnerAccess),
	// Only the owner decides; the approval service keeps its own check.
	method(contract.MethodOpDecide, contract.ScopeAdmin, OwnerAccess,
		act("op", "decide", typ[contract.OpDecideRequest](), "Decide an operation", "op approve", "op deny"),
	),
	method(contract.MethodOpList, contract.ScopeRead, UserAccess),
	// op.submit checks its own scope by operation kind, so the row names none.
	method(contract.MethodOpSubmit, "", UserAccess,
		opAct("hostexec", "request_grant", contract.HostexecGrantArgs{}.OpKind(), typ[contract.HostexecGrantArgs](), "Request access to hosts with a reason. Omit room and to to use your own; omit expires for the default duration.", "hostexec grant"),
		opAct("pr", "open", contract.PrOpenArgs{}.OpKind(), typ[contract.PrOpenArgs](), "Open a pull request with title and base. Body and draft are optional; omit head to use your session branch.", "pr open"),
	),
	method(contract.MethodOpWithdraw, contract.ScopeOperate, UserAccess,
		act("op", "withdraw", typ[contract.OpWithdrawRequest](), "Withdraw your pending operation by id.", "op withdraw"),
	),
	method(contract.MethodPost, contract.ScopeOperate, UserAccess,
		act("post", "send", typ[PostInput](), "Send or update a post. paths holds attachment paths; html names a visual file. Set visualUpdate to update a visual.", "post"),
	),
	method(contract.MethodPostSearch, contract.ScopeRead, OwnerAccess),
	method(contract.MethodProfileList, contract.ScopeRead, OwnerAccess),
	method(contract.MethodProfileRemove, contract.ScopeAdmin, OwnerAccess),
	method(contract.MethodProfileSet, contract.ScopeAdmin, OwnerAccess),
	method(contract.MethodRoomCreate, contract.ScopeAdmin, OwnerAccess,
		act("room", "create", typ[contract.RoomCreateRequest](), "Create a room", "room create"),
	),
	method(contract.MethodRoomList, contract.ScopeRead, OwnerAccess),
	method(contract.MethodRoomPause, contract.ScopeOperate, UserAccess,
		act("room", "pause", typ[contract.RoomPauseRequest](), "Set paused for room.", "room pause", "room resume"),
	),
	method(contract.MethodRoomRepoAdd, contract.ScopeAdmin, OwnerAccess,
		act("room", "add_repo", typ[contract.RoomRepoAddRequest](), "Add a repository", "room repo add"),
	),
	method(contract.MethodRoomRepos, contract.ScopeRead, UserAccess,
		act("room", "repos", typ[contract.RoomReposRequest](), "List repositories in room.", "room repo list"),
	),
	method(contract.MethodRoomSetLimits, contract.ScopeAdmin, OwnerAccess,
		act("room", "limit", typ[contract.RoomSetLimitsRequest](), "Set room limits", "room limit"),
	),
	method(contract.MethodSessionRestart, contract.ScopeOperate, UserAccess,
		act("session", "restart", typ[contract.SessionRestartRequest](), "Replace the failed box for session; unpushed work is lost.", "session restart"),
	),
	method(contract.MethodSessionClose, contract.ScopeOperate, UserAccess,
		act("session", "close", typ[contract.SessionCloseRequest](), "Close session. Set discard to skip its archive.", "session close"),
	),
	method(contract.MethodSessionList, contract.ScopeRead, UserAccess,
		act("session", "list", typ[contract.SessionListRequest](), "List sessions in room.", "session list"),
	),
	method(contract.MethodSessionMove, contract.ScopeOperate, UserAccess,
		act("session", "move", typ[contract.SessionMoveRequest](), "Move session under newParent.", "session move"),
	),
	method(contract.MethodSessionSearch, contract.ScopeRead, OwnerAccess),
	method(contract.MethodSessionSetProfile, contract.ScopeOperate, UserAccess,
		act("session", "set_profile", typ[contract.SessionSetProfileRequest](), "Set profile for session.", "session set-profile"),
	),
	method(contract.MethodSessionStart, contract.ScopeOperate, UserAccess,
		act("session", "start", typ[contract.SessionStartRequest](), "Start a child session. Give room, name and profile. Omit parent to use the caller; repo, goal, brief, size and sameHost are optional.", "session start"),
	),
	method(contract.MethodSessionStop, contract.ScopeOperate, UserAccess,
		act("session", "stop", typ[contract.SessionStopRequest](), "Stop the running turn of session.", "session stop"),
	),
	method(contract.MethodSessionWork, contract.ScopeRead, OwnerAccess),
	method(contract.MethodShellJobEnded, contract.ScopeRead, BoxAccess),
	method(contract.MethodShellOutput, contract.ScopeRead, BoxAccess),
	method(contract.MethodTranscriptAppend, contract.ScopeRead, BoxAccess),
	method(contract.MethodTranscriptFetch, contract.ScopeRead, BoxAccess),
	method(contract.MethodTranscriptRead, contract.ScopeRead, OwnerAccess),
	method(contract.MethodTurnActivity, contract.ScopeRead, BoxAccess),
	method(contract.MethodTurnOutput, contract.ScopeRead, BoxAccess),
	method(contract.MethodWebLogin, contract.ScopeAdmin, OwnerLocalAccess),
}

var methodRows = buildMethodRows(methodTable)

func buildMethodRows(rows []MethodEntry) map[contract.Method]MethodEntry {
	result := make(map[contract.Method]MethodEntry, len(rows))
	for _, row := range rows {
		result[row.Method] = row
	}
	return result
}

// Methods returns the one method table: exactly one row per Method constant,
// in method order. Rows without actions are engine-only or not yet served.
// Callers must not modify the table.
func Methods() []MethodEntry { return methodTable }
