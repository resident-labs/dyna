package machines

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
