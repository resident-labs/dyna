package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/websvc"
)

// ownerCalls are controls used only by authenticated owner UI clients.
var ownerCalls = []string{
	"TermWrite",
	"TermOpen",
	"CreatePairing",
	"SetDeviceScopes",
	"RevokeDevice",
	"ComputerControl",
	"AccountsAdd",
	"SetAccountRepos",
	"UninstallPrivateRuntime",
	"CancelPrivateRuntimeLogin",
	"RespondPrivateRuntimeLogin",
	"StartIssueWork",
	"Send",
	"SendWithReferences",
	"SendToSessionWithReferences",
	"SendToSession",
	"DeclineCallRequest",
	"VoiceStart",
	"VoiceResume",
	"VoiceStop",
	"TermClose",
	"TermResize",
	"ProfileModels",
	"Attach",
	"ArchiveRoom",
	"UnarchiveRoom",
	"RenameRoom",
	"PinRoom",
	"UnpinRoom",
	"DeletePendingSteer",
	"CallRequestShown",
	"MarkRead",
	"MarkReadMany",
	"MarkRoomRead",
	"UpdateNotice",
	"DismissNotice",
	// A probe reads a machine over SSH and stores what it found; the owner
	// starts it from the Machine page.
	"ProbeMachine",
}

// protectOwnerCalls shares caller resolution with other mutations, then
// requires the owner role for controls that no CLI or agent path uses.
func protectOwnerCalls(calls map[string]websvc.Call) {
	for _, name := range ownerCalls {
		call := calls[name]
		calls[name] = func(ctx context.Context, args []json.RawMessage) (any, error) {
			if authority.Of(ctx).Role != authority.Owner {
				return nil, fmt.Errorf("%s is owner only", name)
			}
			return call(ctx, args)
		}
	}
	// This wrapper must be outermost, so the role check sees mutationActor.
	protectCalls(calls, ownerCalls, "using owner controls", nil)
}
