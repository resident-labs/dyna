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
	"HarnessProviderKeys",
	"SetAccountRepos",
	"UninstallPrivateRuntime",
	"CancelPrivateRuntimeLogin",
	"RespondPrivateRuntimeLogin",
	"CancelPrivateRuntimeAccountSwitch",
	"ConfirmPrivateRuntimeAccountSwitch",
	"StartIssueWork",
	"Send",
	"SendToSession",
	"SteerRun",
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
	"UpdateNotice",
	"DismissNotice",
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
