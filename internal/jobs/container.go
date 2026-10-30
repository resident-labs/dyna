package jobs

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"dyna-agent/internal/machines"
)

// NetworkMode is a container network request. Empty keeps Docker's default.
type NetworkMode string

const NetworkNone NetworkMode = "none"

// MountRequest selects a node registration by name. Mode empty inherits the
// registration; ro can reduce a rw registration, but rw cannot widen ro.
type MountRequest struct {
	Name   machines.MountName
	Target string
	Mode   machines.MountMode
}

// ContainerMount is one validated bind mount stored with a queued job.
type ContainerMount struct {
	Source string             `json:"source"`
	Target string             `json:"target"`
	Mode   machines.MountMode `json:"mode"`
}

// ContainerOptions is the validated container access stored with a job.
type ContainerOptions struct {
	Network NetworkMode      `json:"network,omitempty"`
	Mounts  []ContainerMount `json:"mounts,omitempty"`
	Devices []string         `json:"devices,omitempty"`
}

// ParseMountRequests reads NAME:CONTAINER_PATH[:ro|rw] at the engine boundary.
func ParseMountRequests(values []string) ([]MountRequest, error) {
	out := make([]MountRequest, 0, len(values))
	for _, value := range values {
		name, target, ok := strings.Cut(value, ":")
		if !ok {
			return nil, fmt.Errorf("job mount must be NAME:CONTAINER_PATH[:ro|rw], not %q", value)
		}
		parsedName, err := machines.ParseMountName(name)
		if err != nil {
			return nil, err
		}
		request := MountRequest{Name: parsedName, Target: target}
		for _, mode := range []machines.MountMode{machines.MountRO, machines.MountRW} {
			suffix := ":" + string(mode)
			if strings.HasSuffix(request.Target, suffix) {
				request.Target = strings.TrimSuffix(request.Target, suffix)
				request.Mode = mode
				break
			}
		}
		out = append(out, request)
	}
	return out, nil
}

func validateMountTarget(target string) error {
	if target == "" || !path.IsAbs(target) || path.Clean(target) != target || strings.ContainsAny(target, ":\x00") {
		return fmt.Errorf("container mount target %q must be a clean absolute path without a colon", target)
	}
	if target == "/workspace" || strings.HasPrefix(target, "/workspace/") {
		return errors.New("a named mount cannot replace the job's /workspace")
	}
	return nil
}

// resolveContainer validates agent requests against the selected node's
// registrations and returns the host options that the detached job keeps.
func resolveContainer(machine machines.Machine, in Input) (ContainerOptions, error) {
	options := ContainerOptions{Network: in.Network}
	if options.Network != "" && options.Network != NetworkNone {
		return ContainerOptions{}, fmt.Errorf("container network must be none, not %q", options.Network)
	}
	seenTargets := make(map[string]bool, len(in.Mounts))
	for _, request := range in.Mounts {
		if err := validateMountTarget(request.Target); err != nil {
			return ContainerOptions{}, err
		}
		if seenTargets[request.Target] {
			return ContainerOptions{}, fmt.Errorf("container mount target %s is requested twice", request.Target)
		}
		seenTargets[request.Target] = true
		var registered *machines.NamedMount
		nameExists := false
		for i := range machine.Mounts {
			candidate := &machine.Mounts[i]
			if candidate.Name != request.Name {
				continue
			}
			nameExists = true
			if candidate.Room == in.RoomID {
				registered = candidate
				break
			}
		}
		if registered == nil {
			if nameExists {
				return ContainerOptions{}, fmt.Errorf("mount %s is registered for another room, not %s", request.Name, in.RoomID)
			}
			return ContainerOptions{}, fmt.Errorf("unknown mount %s on node %s for room %s", request.Name, machine.Name, in.RoomID)
		}
		if request.Mode != "" && request.Mode != machines.MountRO && request.Mode != machines.MountRW {
			return ContainerOptions{}, fmt.Errorf("mount %s request mode must be ro or rw", request.Name)
		}
		if registered.Mode == machines.MountRO && request.Mode == machines.MountRW {
			return ContainerOptions{}, fmt.Errorf("mount %s is read-only and cannot be requested rw", request.Name)
		}
		mode := registered.Mode
		if request.Mode != "" {
			mode = request.Mode
		}
		options.Mounts = append(options.Mounts, ContainerMount{Source: registered.Source, Target: request.Target, Mode: mode})
	}
	for _, device := range in.Devices {
		if device == "" || !path.IsAbs(device) || path.Clean(device) != device || strings.ContainsRune(device, 0) {
			return ContainerOptions{}, fmt.Errorf("device %q must be a clean absolute path", device)
		}
		allowed := false
		for _, grant := range machine.Devices {
			if grant.Path == device && grant.Room == in.RoomID {
				allowed = true
				break
			}
		}
		if !allowed {
			return ContainerOptions{}, fmt.Errorf("device %s is not allowed on node %s for room %s", device, machine.Name, in.RoomID)
		}
		options.Devices = append(options.Devices, device)
	}
	return options, nil
}
