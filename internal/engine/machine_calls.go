package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/websvc"
)

// machineCalls are the machine writes that act for their caller. SaveNode is
// here rather than in ownerCalls because a room root decides a machine's
// operating settings, as the owner does.
var machineCalls = []string{"SaveNode"}

// hostChecksCommand is the command that allows host checks on a machine, which
// a refusal names so the owner can run it.
const hostChecksCommand = "dyna node edit <name> --allow-host-checks"

// protectMachineCalls lets the owner change any machine field and a room root
// change its operating settings: host checks, the SSH connect timeout and the
// container socket. A root speaks for its room and operates its machines as the
// owner does. The other fields decide where the engine sends work, so they stay
// the owner's, and a repository file, a session or a
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
	protectCalls(calls, machineCalls, "changing a node", nil)
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
		if rootMayPatch(patch) {
			return nil
		}
		return fmt.Errorf("SaveNode is owner only for a node's creation, name, route and switch; the owner changes them, and a room root changes host checks, the SSH connect timeout, the container socket, node queue limits and named resources (for example %s)", hostChecksCommand)
	}
	return fmt.Errorf("SaveNode is owner only; the owner allows host checks with %s", hostChecksCommand)
}

// machinePatchOf reads the patch of a SaveNode call. It reports false when
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

// rootMayPatch reports whether a room root may apply the patch. A root changes
// the operating settings of a machine: host checks, the SSH connect timeout,
// the check temporary folder, a local container runtime socket, the node
// queue's limits (IO-heavy work and disk) and named resources. Those set
// capacity and route no work. The creation, name, switch, route, host, room
// list, labels stay the owner's: they decide which machine the
// engine reaches and which rooms' code runs on it, and the ci label routes
// local CI, so a root could send another room's work, or its own SSH
// credentials, to a host the owner never chose.
func rootMayPatch(patch websvc.MachinePatch) bool {
	return !patch.Create && !patch.Remove && patch.Name == nil && patch.Labels == nil && patch.Enabled == nil && patch.SSH == nil && patch.Rooms == nil && localSocket(patch.ContainerSocket)
}

// localSocket reports whether a patch leaves the container socket alone, restores
// the default, or names a Unix socket on the machine itself. A remote endpoint,
// such as tcp://host:2375, would send workflow containers and repository
// contents of every room that shares the machine to another host, so only the
// owner may set one.
func localSocket(socket *string) bool {
	return socket == nil || *socket == "" || strings.HasPrefix(*socket, "unix://")
}
