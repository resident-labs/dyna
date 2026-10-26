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

// A room root allows host checks as the owner does, and changes no machine
// field that routes work.
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
	// Creating a machine and naming it decide where work goes too.
	if _, err := calls["SaveMachine"](ctx, saveMachineArgs(`{"create":true,"roles":["ci"],"ciSlots":1,"ssh":"pip@hermeslab"}`)); err == nil || !strings.Contains(err.Error(), "owner only") {
		t.Fatalf("a root creating a machine = %v, want an owner-only refusal", err)
	}
	if _, err := calls["SaveMachine"](ctx, saveMachineArgs(`{"name":"renamed"}`)); err == nil || !strings.Contains(err.Error(), "owner only") {
		t.Fatalf("a root renaming a machine = %v, want an owner-only refusal", err)
	}
	// The route host decides which machine the engine reaches, so it stays the
	// owner's even beside an operating setting.
	if _, err := calls["SaveMachine"](ctx, saveMachineArgs(`{"sshConnectTimeoutSeconds":60,"ssh":"pip@elsewhere"}`)); err == nil || !strings.Contains(err.Error(), "owner only") {
		t.Fatalf("a root changing the route host = %v, want an owner-only refusal", err)
	}
	if len(*patches) != 1 {
		t.Fatalf("saved patches = %+v, want the refused patch to write nothing", *patches)
	}
}

// A room root changes a machine's SSH connect timeout and container socket
// with dyna machine edit, as the owner does (D-222). A session cannot.
func TestRoomRootChangesOperatingSettings(t *testing.T) {
	calls, patches := guardedSaveMachine()
	root := localCallerContext(t,
		websvc.Caller{Kind: websvc.Session, Session: "root"},
		authority.Actor{Role: authority.Root, Session: "root", Room: "room_dyna"})
	for _, patch := range []string{`{"sshConnectTimeoutSeconds":60}`, `{"containerSocket":"unix:///run/user/1000/podman/podman.sock"}`} {
		if _, err := calls["SaveMachine"](root, saveMachineArgs(patch)); err != nil {
			t.Fatalf("a root sending %s = %v, want it allowed", patch, err)
		}
	}
	if len(*patches) != 2 {
		t.Fatalf("saved patches = %+v, want both settings", *patches)
	}
	// A remote container endpoint sends every room's workflow containers to
	// another host, so it stays the owner's; restoring the default is allowed.
	if _, err := calls["SaveMachine"](root, saveMachineArgs(`{"containerSocket":"tcp://other-host:2375"}`)); err == nil || !strings.Contains(err.Error(), "owner only") {
		t.Fatalf("a root setting a TCP container endpoint = %v, want an owner-only refusal", err)
	}
	if _, err := calls["SaveMachine"](root, saveMachineArgs(`{"containerSocket":""}`)); err != nil {
		t.Fatalf("a root restoring the default socket = %v, want it allowed", err)
	}
	*patches = (*patches)[:2]
	session := localCallerContext(t,
		websvc.Caller{Kind: websvc.Session, Session: "ses_1"},
		authority.Actor{Role: authority.Session, Session: "ses_1", Room: "room_dyna"})
	if _, err := calls["SaveMachine"](session, saveMachineArgs(`{"sshConnectTimeoutSeconds":60}`)); err == nil || !strings.Contains(err.Error(), "owner only") {
		t.Fatalf("a session changing the timeout = %v, want an owner-only refusal", err)
	}
	if len(*patches) != 2 {
		t.Fatalf("saved patches = %+v, want the session's patch refused", *patches)
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

// A room root cannot slip a room-routing change through beside a host-check
// change: the room list decides which rooms' CI work the machine takes, so it
// is the owner's.
func TestRoomRootCannotChangeRoomsWithHostChecks(t *testing.T) {
	calls, patches := guardedSaveMachine()
	ctx := localCallerContext(t,
		websvc.Caller{Kind: websvc.Session, Session: "root"},
		authority.Actor{Role: authority.Root, Session: "root", Room: "room_dyna"})

	if _, err := calls["SaveMachine"](ctx, saveMachineArgs(`{"allowHostChecks":true,"rooms":[]}`)); err == nil || !strings.Contains(err.Error(), "owner only") {
		t.Fatalf("a root clearing the room list = %v, want an owner-only refusal", err)
	}
	if len(*patches) != 0 {
		t.Fatalf("saved patches = %+v, want none", *patches)
	}
}
