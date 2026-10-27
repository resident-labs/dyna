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
