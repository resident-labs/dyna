package engine

import (
	"context"
	"encoding/json"
	"log"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/websvc"
)

// protectCalls makes each named call act for its mutation actor: the
// resolved caller, or, for a report-only unverified caller, its claimed
// session (mutationActor). So a CLI call and a web call meet the same
// authority checks, which read authority.Of(ctx). with adds anything else
// the call needs on ctx; by names who acted.
func protectCalls(calls map[string]websvc.Call, names []string, doing string, with func(ctx context.Context, by string) context.Context) {
	for _, name := range names {
		call := calls[name]
		calls[name] = func(ctx context.Context, args []json.RawMessage) (any, error) {
			caller, local := websvc.CallerFromContext(ctx)
			actor, by, err := mutationActor(caller, local, authority.Of(ctx), callspec.ClaimOf(ctx), enforceCallers, log.Printf, doing)
			if err != nil {
				return nil, err
			}
			ctx = authority.WithActor(ctx, actor)
			if with != nil {
				ctx = with(ctx, by)
			}
			return call(ctx, args)
		}
	}
}

var goalCalls = []string{"AddGoal", "AddGoalItem", "MoveGoalItem", "EditGoalSelectors", "EditGoalItem", "EditGoal", "FinishGoal", "ExpediteGoal", "SetGoalItemState", "NoteGoal", "AddRule", "EditRule", "RemoveRule"}

// protectGoalCalls applies the same caller policy to CLI and web mutations.
func protectGoalCalls(calls map[string]websvc.Call) {
	protectCalls(calls, goalCalls, "changing goals or rules", roomsvc.WithGoalAuthor)
}

// sessionCalls are the session lifecycle calls that the CLI and the web app
// share. The Manager checks each one against the caller on ctx.
var sessionCalls = []string{"StopSession", "CloseSession", "ForceCloseSession", "CloseSessionOptions", "ReopenSession", "PromoteSession", "AdoptSession", "RenameSession", "StartSubagent", "DismissSession", "DismissPendingPost", "PauseRoomSessions", "ResumeRoomSessions", "ClearDormantSessions",
	"TransferSession", "CancelSessionTransfer", "SubmitTransferHandoff", "ConsolidateSessions", "ResumeSessionConsolidation", "CancelSessionConsolidation"}

// protectSessionCalls applies the caller policy to the session calls.
func protectSessionCalls(calls map[string]websvc.Call) {
	protectCalls(calls, sessionCalls, "changing a session", nil)
}
