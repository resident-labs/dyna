package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/websvc"
)

// hostChecksCommand is the command that allows host checks on a machine, which
// a refusal names so the owner can run it.
const hostChecksCommand = "dyna node edit <name> --allow-host-checks"

// checkMachineWrite is the machine policy's check: the owner changes any
// machine field and a room root changes its operating settings — host checks,
// the SSH connect timeout, the container socket, and room-scoped container
// registrations. A root speaks for its room and operates its machines as the
// owner does. The other fields decide where the engine sends work, so they stay the owner's, and a repository file, a
// session or a workflow worker passes neither.
func checkMachineWrite(ctx context.Context, _ string, args []json.RawMessage) error {
	actor := authority.Of(ctx)
	if actor.Role == authority.Owner {
		return nil
	}
	patch, ok := machinePatchOf(args)
	if !ok {
		return nil
	}
	if actor.Role == authority.Root {
		if rootMayPatch(patch) {
			if containerAccessForRoom(patch, actor.Room) {
				return nil
			}
			return fmt.Errorf("SaveNode: a room root registers mounts and devices for its own room only")
		}
		return fmt.Errorf("SaveNode is owner only for a node's creation, name, route and switch; the owner changes them, and a room root changes host checks, the SSH connect timeout, the container socket, node queue limits, named resources, and its own room's mounts and devices (for example %s)", hostChecksCommand)
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
// queue's limits (IO-heavy work and disk), named resources, and room-scoped
// container registrations. Those set capacity and route no work. The creation,
// name, switch, route, host, room list, labels stay the owner's: they decide which machine the
// engine reaches and which rooms' code runs on it, and the ci label routes
// local CI, so a root could send another room's work, or its own SSH
// credentials, to a host the owner never chose.
func rootMayPatch(patch websvc.MachinePatch) bool {
	return !patch.Create && !patch.Remove && patch.Name == nil && patch.Labels == nil && patch.Enabled == nil && patch.SSH == nil && patch.Rooms == nil && localSocket(patch.ContainerSocket)
}

// containerAccessForRoom limits a root's registrations to the room it speaks for.
func containerAccessForRoom(patch websvc.MachinePatch, room string) bool {
	if patch.Mounts != nil {
		for _, mount := range *patch.Mounts {
			if mount.Room != room {
				return false
			}
		}
	}
	if patch.Devices != nil {
		for _, device := range *patch.Devices {
			if device.Room != room {
				return false
			}
		}
	}
	return true
}

// localSocket reports whether a patch leaves the container socket alone, restores
// the default, or names a Unix socket on the machine itself. A remote endpoint,
// such as tcp://host:2375, would send workflow containers and repository
// contents of every room that shares the machine to another host, so only the
// owner may set one.
func localSocket(socket *string) bool {
	return socket == nil || *socket == "" || strings.HasPrefix(*socket, "unix://")
}
