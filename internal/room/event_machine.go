package room

import (
	"strconv"
)

// MachineEvent tells a room's root that a machine stopped taking CI work. It
// carries the sizes the probe read and the floor it missed, so the card says
// what to free without reading the post text.
type MachineEvent struct {
	Name string `json:"name"`
	// FreeBytes and FloorBytes are the free space of the machine's /var/tmp and
	// the floor that free space missed.
	FreeBytes  int `json:"freeBytes"`
	FloorBytes int `json:"floorBytes"`
	// Summary is the short facts line the agent text reads: the two sizes as a
	// person reads them.
	Summary string `json:"summary"`
	// AtMS is when Dyna posted the alert, in milliseconds since the Unix epoch.
	// Two low-disk periods of one machine are two alerts, because each has its
	// own time.
	AtMS int `json:"atMs"`
}

func (MachineEvent) eventFamily() EventFamily { return FamilyMachine }
func (MachineEvent) eventType() EventType     { return TypeLowDisk }

// eventSubject is the machine, so the alerts of one machine fold into one card.
func (m MachineEvent) eventSubject() string { return m.Name }

// eventDedupe keys one alert per machine and probe, so a retried alert of the
// same transition posts once and a later period posts again.
func (m MachineEvent) eventDedupe() string {
	return strconv.Itoa(m.AtMS)
}

// MachineCacheEvent tells a room that a run switched linked CI caches back to
// managed real directories. It names the machine and the cache kinds in words,
// never a path or a link target, which belong to the machine and not the room.
type MachineCacheEvent struct {
	Name string `json:"name"`
	// Caches names each repaired cache in clear words, such as "Go build
	// cache".
	Caches []string `json:"caches"`
	// Summary is the short facts line the agent text reads.
	Summary string `json:"summary"`
	// AtMS is when Dyna repaired the caches, in milliseconds since the Unix
	// epoch. Each repair is its own post, because each has its own time.
	AtMS int `json:"atMs"`
}

func (MachineCacheEvent) eventFamily() EventFamily { return FamilyMachine }
func (MachineCacheEvent) eventType() EventType     { return TypeCacheRepaired }

// eventSubject is the machine, so repairs of one machine fold into one card.
func (m MachineCacheEvent) eventSubject() string { return m.Name }

// eventDedupe keys one repair per machine and time, so a retried post of the
// same repair posts once.
func (m MachineCacheEvent) eventDedupe() string { return strconv.Itoa(m.AtMS) }

// MachineCacheCleanFailedEvent tells a room that Dyna could not remove one
// managed CI path. Reason is one bounded line, not the runtime's output flood.
type MachineCacheCleanFailedEvent struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
	// Generation identifies this failure period. Every successful real cleanup
	// advances it; zero means no cleanup has succeeded yet.
	Generation int64 `json:"generation,omitempty"`
	// AtMS is when Dyna first observed this failure, in milliseconds since the
	// Unix epoch.
	AtMS int `json:"atMs"`
}

func (MachineCacheCleanFailedEvent) eventFamily() EventFamily { return FamilyMachine }
func (MachineCacheCleanFailedEvent) eventType() EventType     { return TypeCacheCleanFailed }

// eventSubject folds cleanup failures with the machine's other cache facts.
func (m MachineCacheCleanFailedEvent) eventSubject() string { return m.Name }

// eventDedupe keeps hourly retries of one continuous failure together. A
// success changes the generation, so an identical later incident posts again.
func (m MachineCacheCleanFailedEvent) eventDedupe() string {
	return strconv.FormatInt(m.Generation, 10) + "\x00" + m.Path + "\x00" + m.Reason
}

// MachineScratchEvent tells a room what a sweep of a machine's scratch folders
// freed. It carries counts and sizes, never a path, which belongs to the
// machine and not the room.
type MachineScratchEvent struct {
	Name string `json:"name"`
	// Folders counts the folders removed because no file in them changed for
	// the retention period. Builds counts the build outputs removed because
	// the machine was under its CI disk floor.
	Folders int `json:"folders"`
	Builds  int `json:"builds"`
	// FreedBytes is the space the sweep freed.
	FreedBytes int `json:"freedBytes"`
	// Summary is the short facts line the agent text reads.
	Summary string `json:"summary"`
	// AtMS is when Dyna posted the notice, in milliseconds since the Unix epoch.
	AtMS int `json:"atMs"`
}

func (MachineScratchEvent) eventFamily() EventFamily { return FamilyMachine }
func (MachineScratchEvent) eventType() EventType     { return TypeScratchFreed }

// eventSubject is the machine, so sweeps of one machine fold into one card.
func (m MachineScratchEvent) eventSubject() string { return m.Name }

// eventDedupe keys one notice per machine and time, so a retried post posts once.
func (m MachineScratchEvent) eventDedupe() string { return strconv.Itoa(m.AtMS) }

// MachineLimitsEvent tells a room that this machine cannot give each room its
// own resource slice. The room's work then shares the agent slice with every
// other room and has no process cap. It carries the reason in plain words,
// because the card explains what is missing, not a path or a log line.
type MachineLimitsEvent struct {
	// Reason is why the limits are off, for example a controller that systemd
	// did not delegate to this user.
	Reason string `json:"reason"`
	// AtMS is when Dyna first noticed, in milliseconds since the Unix epoch.
	AtMS int `json:"atMs"`
}

func (MachineLimitsEvent) eventFamily() EventFamily { return FamilyMachine }
func (MachineLimitsEvent) eventType() EventType     { return TypeLimitsOff }

// eventSubject keeps the notice out of any other machine note's rows.
func (MachineLimitsEvent) eventSubject() string { return "room-limits" }

// eventDedupe keys one notice per reason, so the same cause posts once and a
// changed cause posts again.
func (m MachineLimitsEvent) eventDedupe() string { return m.Reason }

// RoomCapKind names one cap of a room's resource slice, after the cgroup file
// that holds the limit.
type RoomCapKind string

// The caps of a room's slice.
const (
	// RoomCapMemoryHigh is where the kernel starts to throttle and reclaim
	// the room's memory.
	RoomCapMemoryHigh RoomCapKind = "memory.high"
	// RoomCapMemoryMax is the hard memory limit: the OOM killer takes a
	// process inside the room, never outside it.
	RoomCapMemoryMax RoomCapKind = "memory.max"
	// RoomCapPidsMax bounds the room's processes: a fork above it fails inside
	// the room only.
	RoomCapPidsMax RoomCapKind = "pids.max"
)

// RoomCapEvent tells a room's root and the owner that the room's work is
// close to one cap of its resource slice: the kernel soon throttles its
// memory or refuses its forks. It carries the two sizes and the room's
// biggest process trees, so the card names what to rein in without reading
// the post text.
type RoomCapEvent struct {
	// Room is the room's name, as the owner reads it.
	Room string `json:"room"`
	// Cap is the cap the room is close to.
	Cap RoomCapKind `json:"cap"`
	// Current and Limit are the room's use and the cap itself: bytes for the
	// memory caps, processes for pids.max. The card reads them; the agent
	// text reads Summary.
	Current int `json:"current"`
	Limit   int `json:"limit"`
	// Summary is the short facts line the agent text reads.
	Summary string `json:"summary"`
	// TopByMemory and TopByCount list the room's biggest process trees, by
	// resident memory and by process count.
	TopByMemory []RoomCapTree `json:"topByMemory,omitempty"`
	TopByCount  []RoomCapTree `json:"topByCount,omitempty"`
	// Detail is the tree lines the room's root reads below the alert line.
	Detail string `json:"detail,omitempty"`
	// AtMS is when Dyna posted the alert, in milliseconds since the Unix
	// epoch. Two cap periods of one room are two alerts, because each has its
	// own time.
	AtMS int `json:"atMs"`
}

func (RoomCapEvent) eventFamily() EventFamily { return FamilyMachine }
func (RoomCapEvent) eventType() EventType     { return TypeRoomCap }

// eventSubject is the room, so the alerts of one room fold into one card.
func (e RoomCapEvent) eventSubject() string { return e.Room }

// eventDedupe keys one alert per room, cap and time, so a retried alert of
// the same crossing posts once and a later crossing posts again.
func (e RoomCapEvent) eventDedupe() string {
	return string(e.Cap) + "\x00" + strconv.Itoa(e.AtMS)
}

// RoomCapTree is one process tree inside the room's slice, as the alert lists
// it: the tree's root command and PID, with the memory and process count of
// everything under it.
type RoomCapTree struct {
	Command string `json:"command"`
	PID     int    `json:"pid"`
	// Memory is the resident memory of the whole tree, in bytes.
	Memory int `json:"memory"`
	// Count is the processes in the tree.
	Count int `json:"count"`
}
