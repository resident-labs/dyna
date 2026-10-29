package machines

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// MountName is the owner-assigned name of a host folder on one node and room.
type MountName string

// MountMode is the access Docker gives a container to a registered folder.
type MountMode string

const (
	MountRO MountMode = "ro"
	MountRW MountMode = "rw"
)

// NamedMount registers a host folder for one room. Job submitters name only
// Name and a container target; Source always comes from this registry.
type NamedMount struct {
	Name   MountName `json:"name"`
	Source string    `json:"source"`
	Room   string    `json:"room"`
	Mode   MountMode `json:"mode"`
}

// DeviceGrant allows one room's containers to use one host device path.
type DeviceGrant struct {
	Path string `json:"path"`
	Room string `json:"room"`
}

// MountRef identifies one room's named mount for removal.
type MountRef struct {
	Name MountName `json:"name"`
	Room string    `json:"room"`
}

// DeviceRef identifies one room's allowed device for removal.
type DeviceRef struct {
	Path string `json:"path"`
	Room string `json:"room"`
}

// ParseMountName validates a registered mount name at an external boundary.
func ParseMountName(name string) (MountName, error) {
	if !resourceNamePattern.MatchString(name) {
		return "", fmt.Errorf("mount name %q must use lowercase letters, digits, '-', '_' or '.', and be at most 64 characters", name)
	}
	return MountName(name), nil
}

// ParseNamedMount reads NAME=HOST_PATH:ro|rw for one room.
func ParseNamedMount(value, room string) (NamedMount, error) {
	name, spec, ok := strings.Cut(value, "=")
	if !ok {
		return NamedMount{}, fmt.Errorf("mount must be NAME=HOST_PATH:ro|rw, not %q", value)
	}
	parsedName, err := ParseMountName(name)
	if err != nil {
		return NamedMount{}, err
	}
	colon := strings.LastIndex(spec, ":")
	if colon < 0 {
		return NamedMount{}, fmt.Errorf("mount must end in :ro or :rw, not %q", value)
	}
	mount := NamedMount{Name: parsedName, Source: spec[:colon], Room: room, Mode: MountMode(spec[colon+1:])}
	if err := mount.Validate(); err != nil {
		return NamedMount{}, err
	}
	return mount, nil
}

// Validate checks a mount decoded from an external machine patch or SQLite.
func (m NamedMount) Validate() error {
	if _, err := ParseMountName(string(m.Name)); err != nil {
		return err
	}
	if err := validateRoomList([]string{m.Room}); err != nil {
		return fmt.Errorf("mount %s: %w", m.Name, err)
	}
	if err := cleanAbsolutePath(m.Source); err != nil {
		return fmt.Errorf("mount %s source: %w", m.Name, err)
	}
	if m.Mode != MountRO && m.Mode != MountRW {
		return fmt.Errorf("mount %s mode must be ro or rw, not %q", m.Name, m.Mode)
	}
	return nil
}

// ParseMountRef reads the name and room of a registration to remove.
func ParseMountRef(name, room string) (MountRef, error) {
	parsedName, err := ParseMountName(name)
	if err != nil {
		return MountRef{}, err
	}
	ref := MountRef{Name: parsedName, Room: room}
	if err := ref.Validate(); err != nil {
		return MountRef{}, err
	}
	return ref, nil
}

// Validate checks a mount reference decoded from an external patch.
func (r MountRef) Validate() error {
	if _, err := ParseMountName(string(r.Name)); err != nil {
		return err
	}
	if err := validateRoomList([]string{r.Room}); err != nil {
		return fmt.Errorf("mount %s: %w", r.Name, err)
	}
	return nil
}

// ParseDeviceRef reads the path and room of a device grant to remove.
func ParseDeviceRef(pathValue, room string) (DeviceRef, error) {
	ref := DeviceRef{Path: pathValue, Room: room}
	if err := ref.Validate(); err != nil {
		return DeviceRef{}, err
	}
	return ref, nil
}

// Validate checks a device reference decoded from an external patch.
func (r DeviceRef) Validate() error {
	if err := cleanAbsolutePath(r.Path); err != nil {
		return fmt.Errorf("device: %w", err)
	}
	if err := validateRoomList([]string{r.Room}); err != nil {
		return fmt.Errorf("device %s: %w", r.Path, err)
	}
	return nil
}

// ParseDeviceGrant reads a host device path for one room.
func ParseDeviceGrant(device, room string) (DeviceGrant, error) {
	grant := DeviceGrant{Path: device, Room: room}
	if err := grant.Validate(); err != nil {
		return DeviceGrant{}, err
	}
	return grant, nil
}

// Validate checks a device grant decoded from an external patch or SQLite.
func (d DeviceGrant) Validate() error {
	if err := cleanAbsolutePath(d.Path); err != nil {
		return fmt.Errorf("device: %w", err)
	}
	if err := validateRoomList([]string{d.Room}); err != nil {
		return fmt.Errorf("device %s: %w", d.Path, err)
	}
	return nil
}

func cleanAbsolutePath(value string) error {
	if value == "" || !path.IsAbs(value) || path.Clean(value) != value || strings.ContainsAny(value, ":\x00") {
		return errors.New("path must be a clean absolute path without a colon")
	}
	return nil
}

func validateContainerRegistrations(mounts []NamedMount, devices []DeviceGrant) error {
	type mountKey struct {
		name MountName
		room string
	}
	seenMounts := make(map[mountKey]bool, len(mounts))
	for _, mount := range mounts {
		if err := mount.Validate(); err != nil {
			return err
		}
		key := mountKey{name: mount.Name, room: mount.Room}
		if seenMounts[key] {
			return fmt.Errorf("mount %s is registered twice for room %s", mount.Name, mount.Room)
		}
		seenMounts[key] = true
	}
	type deviceKey struct {
		path string
		room string
	}
	seenDevices := make(map[deviceKey]bool, len(devices))
	for _, device := range devices {
		if err := device.Validate(); err != nil {
			return err
		}
		key := deviceKey{path: device.Path, room: device.Room}
		if seenDevices[key] {
			return fmt.Errorf("device %s is registered twice for room %s", device.Path, device.Room)
		}
		seenDevices[key] = true
	}
	return nil
}
