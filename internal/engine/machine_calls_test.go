package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/websvc"
)

// guardedSaveMachine returns the SaveMachine call with the engine's caller and
// authority wrappers, and a pointer to the patches the saved call receives.
func guardedSaveMachine() (map[string]websvc.Call, *[]websvc.MachinePatch) {
	patches := &[]websvc.MachinePatch{}
	calls := map[string]websvc.Call{"SaveMachine": func(_ context.Context, args []json.RawMessage) (any, error) {
		var patch websvc.MachinePatch
		if err := json.Unmarshal(args[1], &patch); err != nil {
			return nil, err
		}
		*patches = append(*patches, patch)
		return nil, nil
	}}
	protectMachineCalls(calls)
	return calls, patches
}

// saveMachineArgs is one SaveMachine call for machine m_1 with this patch.
func saveMachineArgs(patch string) []json.RawMessage {
	return []json.RawMessage{json.RawMessage(`"m_1"`), json.RawMessage(patch)}
}

// A room root allows host checks as the owner does, and changes no other
// machine field.
func TestRoomRootGrantsHostChecks(t *testing.T) {
	calls, patches := guardedSaveMachine()
	ctx := localCallerContext(t,
		websvc.Caller{Kind: websvc.Session, Session: "root"},
		authority.Actor{Role: authority.Root, Session: "root", Room: "room_dyna"})

	if _, err := calls["SaveMachine"](ctx, saveMachineArgs(`{"allowHostChecks":true}`)); err != nil {
		t.Fatalf("a root granting host checks = %v, want it allowed", err)
	}
	if len(*patches) != 1 || (*patches)[0].AllowHostChecks == nil || !*(*patches)[0].AllowHostChecks {
		t.Fatalf("saved patches = %+v, want the host-check change", *patches)
	}
	// Roles, slots, route and switch decide where the engine sends work, so
	// they stay the owner's.
	if _, err := calls["SaveMachine"](ctx, saveMachineArgs(`{"ciSlots":2}`)); err == nil || !strings.Contains(err.Error(), "owner only") {
		t.Fatalf("a root changing CI slots = %v, want an owner-only refusal", err)
	}
}

// A session cannot allow host checks, and the refusal names the owner's command.
func TestSessionCannotGrantHostChecks(t *testing.T) {
	calls, patches := guardedSaveMachine()
	ctx := localCallerContext(t,
		websvc.Caller{Kind: websvc.Session, Session: "ses_1"},
		authority.Actor{Role: authority.Session, Session: "ses_1", Room: "room_dyna"})

	_, err := calls["SaveMachine"](ctx, saveMachineArgs(`{"allowHostChecks":true}`))
	if err == nil || !strings.Contains(err.Error(), "owner only") || !strings.Contains(err.Error(), hostChecksCommand) {
		t.Fatalf("a session allowing host checks = %v, want an owner-only refusal that names %s", err, hostChecksCommand)
	}
	if len(*patches) != 0 {
		t.Fatalf("saved patches = %+v, want none", *patches)
	}
}
