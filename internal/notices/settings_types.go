package notices

import "time"

// DiskPath is one location below the free-space floor.
type DiskPath struct {
	Path      string `json:"path"`
	FreeBytes int64  `json:"freeBytes"`
}

// DiskFacts is a low-disk notice: the locations that are filling, the free
// space each has left, and what the cleanup pass that raised it did.
type DiskFacts struct {
	Paths []DiskPath `json:"paths" tstype:"DiskPath[] | null"`
	// FreedBytes is what the pass freed.
	FreedBytes int64 `json:"freedBytes,omitempty"`
	// Kept lists why cleanup kept items, as "reason: count".
	Kept []string `json:"kept,omitempty"`
	// MaintenanceFailed reports a pass that could not finish.
	MaintenanceFailed bool `json:"maintenanceFailed,omitempty"`
}

// ProviderFacts is a provider limit: the profile that reached it and when the
// window resets. A zero ResetsAt means the provider named no time.
type ProviderFacts struct {
	Profile  string    `json:"profile"`
	ResetsAt time.Time `json:"resetsAt,omitempty"`
}

// SyncFacts is a remote history problem: the repository and the lanes it
// concerns. A split names the two lanes that no longer share a history; a
// rewrite names the one lane whose history was replaced.
type SyncFacts struct {
	Repo  string `json:"repo"`
	Lane  string `json:"lane"`
	Other string `json:"other,omitempty"`
	// Rewritten reports a lane whose history was replaced, not a split.
	Rewritten bool `json:"rewritten,omitempty"`
}

// TunnelFacts is a missing managed-tunnel connector: the binary the tunnel
// service tried to run and the PATH it searched, so the owner can see where
// an install is missing.
type TunnelFacts struct {
	Binary string `json:"binary"`
	Path   string `json:"path"`
}

// Notice is one owner-facing alert. The fields a card shows are typed facts of
// the notice's kind, so no card reads a stored sentence: a disk notice carries
// Disk, a provider notice carries Provider and a sync notice carries Sync.
// Command is an optional shell command the owner can copy. Text is the stored
// line of a notice saved before the typed facts, and stays empty on a new one.
type Notice struct {
	ID       string         `json:"id"`
	Key      string         `json:"key"`
	Kind     NoticeKind     `json:"kind"`
	Severity Severity       `json:"severity" tstype:"NoticeSeverity"`
	Disk     *DiskFacts     `json:"disk,omitempty"`
	Provider *ProviderFacts `json:"provider,omitempty"`
	Sync     *SyncFacts     `json:"sync,omitempty"`
	Tunnel   *TunnelFacts   `json:"tunnel,omitempty"`
	Command  string         `json:"command,omitempty"`
	At       time.Time      `json:"at"`
	// TODO: legacy/shim remove later — a notice saved before the typed facts
	// carries its sentence here; the Home card shows it until it ages out.
	Text string `json:"text,omitempty"`
}
