package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/websvc"
)

// machineCalls are the machine writes that act for their caller. SaveMachine is
// here rather than in ownerCalls because a room root decides one machine field,
// the host-check permission, as the owner does.
var machineCalls = []string{"SaveMachine"}

// hostChecksCommand is the command that allows host checks on a machine, which
// a refusal names so the owner can run it.
const hostChecksCommand = "dyna machine edit <name> --allow-host-checks"

// protectMachineCalls lets the owner change any machine field and a room root
// change only the host-check permission. A root speaks for its room, and host
// checks run the repositories that room trusts, so the root decides that
// permission as the owner does. The other fields decide where the engine sends
// work, so they stay the owner's, and a repository file, a session or a
// workflow worker passes neither.
func protectMachineCalls(calls map[string]websvc.Call) {
	for _, name := range machineCalls {
		call := calls[name]
		calls[name] = func(ctx context.Context, args []json.RawMessage) (any, error) {
			if err := checkMachineWrite(authority.Of(ctx), args); err != nil {
				return nil, err
			}
			return call(ctx, args)
		}
	}
	// This wrapper must be outermost, so the check sees mutationActor.
	protectCalls(calls, machineCalls, "changing a machine", nil)
}

// checkMachineWrite refuses a machine change that the actor may not make. A
// call without a readable patch passes here, because the binder refuses it.
func checkMachineWrite(actor authority.Actor, args []json.RawMessage) error {
	if actor.Role == authority.Owner {
		return nil
	}
	patch, ok := machinePatchOf(args)
	if !ok {
		return nil
	}
	if actor.Role == authority.Root {
		if onlyHostChecks(patch) {
			return nil
		}
		return fmt.Errorf("SaveMachine is owner only for a machine's creation, name, roles, slots, route and switch; the owner changes them, and a room root changes host checks with %s", hostChecksCommand)
	}
	return fmt.Errorf("SaveMachine is owner only; the owner allows host checks with %s", hostChecksCommand)
}

// machinePatchOf reads the patch of a SaveMachine call. It reports false when
// the call carries no readable patch.
func machinePatchOf(args []json.RawMessage) (websvc.MachinePatch, bool) {
	if len(args) != 2 {
		return websvc.MachinePatch{}, false
	}
	var patch websvc.MachinePatch
	if err := json.Unmarshal(args[1], &patch); err != nil {
		return websvc.MachinePatch{}, false
	}
	return patch, true
}

// onlyHostChecks reports whether the patch changes the host-check permission
// and no other machine field. The room list, the suites, the name and the
// creation of a machine decide where the engine sends work, which is the
// owner's to set.
func onlyHostChecks(patch websvc.MachinePatch) bool {
	return !patch.Create && !patch.Remove && patch.Name == nil && patch.Suites == nil && patch.AllowHostChecks != nil && patch.Enabled == nil && patch.Roles == nil && patch.CISlots == nil && patch.SSH == nil && patch.Rooms == nil
}
