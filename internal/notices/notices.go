// Package notices stores machine-wide owner notices for the Dyna home screen.
package notices

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"dyna-agent/internal/store"
)

// NoticeKind is what an owner notice is about.
type NoticeKind string

// Notice kinds.
const (
	// KindDisk is the data or worktree disk running low.
	KindDisk NoticeKind = "disk"
	// KindProvider is a profile whose provider limit was reached.
	KindProvider NoticeKind = "provider"
	// KindSync is a remote history split that needs the owner's eye.
	KindSync NoticeKind = "sync"
	// KindTunnel is the managed tunnel connector being missing, so remote
	// access stays down until the owner installs it.
	KindTunnel NoticeKind = "tunnel"
)

// noticeKinds lists every kind, so a stored kind can be checked on read.
var noticeKinds = []NoticeKind{KindDisk, KindProvider, KindSync, KindTunnel}

// Severity is how much an owner notice matters.
type Severity string

// Severities.
const (
	// SeverityWarn is a condition the owner should fix soon.
	SeverityWarn Severity = "warn"
	// SeverityError is a condition that already blocks work.
	SeverityError Severity = "error"
)

// severities lists every severity, so a stored severity can be checked.
var severities = []Severity{SeverityWarn, SeverityError}

// DiskPath is one location below the free-space floor.
type DiskPath struct {
	Path      string `json:"path"`
	FreeBytes int64  `json:"freeBytes"`
}

// DiskFacts is a low-disk notice: the locations that are filling, the free
// space each has left, and what the cleanup pass that raised it did.
type DiskFacts struct {
	Paths []DiskPath `json:"paths"`
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
	Severity Severity       `json:"severity"`
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

// Line is the notice as one line for logs and the CLI, built from its typed
// facts rather than stored text.
func (n Notice) Line() string {
	switch {
	case n.Disk != nil:
		parts := ""
		for i, p := range n.Disk.Paths {
			if i > 0 {
				parts += ", "
			}
			parts += fmt.Sprintf("%s (%d bytes free)", p.Path, p.FreeBytes)
		}
		line := "Disk space is low in " + parts + ". Maintenance freed " + fmt.Sprintf("%d bytes", n.Disk.FreedBytes) + "."
		if len(n.Disk.Kept) > 0 {
			line += " Items kept: " + strings.Join(n.Disk.Kept, "; ") + "."
		}
		if n.Disk.MaintenanceFailed {
			line += " Some maintenance failed; see the host log."
		}
		return line
	case n.Provider != nil:
		line := n.Provider.Profile + ": provider limit reached"
		if !n.Provider.ResetsAt.IsZero() {
			line += ", resets " + n.Provider.ResetsAt.Local().Format("15:04")
		}
		return line + "."
	case n.Sync != nil:
		if n.Sync.Rewritten {
			return "Remote history was rewritten in " + n.Sync.Repo + ": " + n.Sync.Lane + "."
		}
		other := n.Sync.Other
		if other == "" {
			other = "another remote"
		}
		return "Remote history split in " + n.Sync.Repo + " between " + n.Sync.Lane + " and " + other + "."
	case n.Tunnel != nil:
		return "The managed tunnel cannot find " + n.Tunnel.Binary + ", so the app stays unreachable from away. It searched PATH " + n.Tunnel.Path + "."
	}
	return n.Text
}

// validate rejects a notice whose facts do not match its kind: a card reads the
// facts, so a missing one would leave it blank.
func (n Notice) validate() error {
	if !slices.Contains(noticeKinds, n.Kind) {
		return fmt.Errorf("notice kind %q is not registered", n.Kind)
	}
	if !slices.Contains(severities, n.Severity) {
		return fmt.Errorf("notice severity %q is not registered", n.Severity)
	}
	if n.Key == "" {
		return fmt.Errorf("a %s notice needs a key", n.Kind)
	}
	set := 0
	for _, has := range []bool{n.Disk != nil, n.Provider != nil, n.Sync != nil, n.Tunnel != nil} {
		if has {
			set++
		}
	}
	if set != 1 {
		return fmt.Errorf("a %s notice carries exactly one of its typed facts", n.Kind)
	}
	switch n.Kind {
	case KindDisk:
		if n.Disk == nil || len(n.Disk.Paths) == 0 {
			return fmt.Errorf("a disk notice needs the paths that are low")
		}
	case KindProvider:
		if n.Provider == nil || n.Provider.Profile == "" {
			return fmt.Errorf("a provider notice needs the profile")
		}
	case KindSync:
		if n.Sync == nil || n.Sync.Repo == "" || n.Sync.Lane == "" {
			return fmt.Errorf("a sync notice needs the repository and lane")
		}
	case KindTunnel:
		if n.Tunnel == nil || n.Tunnel.Binary == "" {
			return fmt.Errorf("a tunnel notice needs the missing binary")
		}
	}
	return nil
}

// Store keeps machine-wide notices in dyna.db under Dir.
type Store struct{ Dir string }

func DefaultStore() Store { return Store{Dir: store.DataDir()} }

// Path is the previous release's notice copy. Watchers use Revision.
func (s Store) Path() string { return filepath.Join(s.Dir, "notices.json") }

// Post adds n. A notice with the same Key is replaced in place, so each key
// shows once with its newest facts and keeps its ID.
func (s Store) Post(n Notice) (Notice, error) {
	if err := n.validate(); err != nil {
		return Notice{}, err
	}
	if n.At.IsZero() {
		n.At = time.Now()
	}
	err := s.update(func(list *[]Notice) {
		if i := slices.IndexFunc(*list, func(old Notice) bool { return old.Key == n.Key }); i >= 0 {
			n.ID = (*list)[i].ID
			(*list)[i] = n
			return
		}
		var b [8]byte
		_, _ = rand.Read(b[:])
		n.ID = "ntc_" + hex.EncodeToString(b[:])
		*list = append(*list, n)
	})
	return n, err
}

// List returns the notices, oldest first.
func (s Store) List() ([]Notice, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	return s.snapshot(db)
}

// Dismiss removes the notice. A later Post with the same Key shows it again.
func (s Store) Dismiss(id string) error {
	return s.update(func(list *[]Notice) {
		*list = slices.DeleteFunc(*list, func(n Notice) bool { return n.ID == id })
	})
}

// DismissKey removes the notice with this Key when one is showing, so a
// condition that healed clears its own alert without knowing its ID.
func (s Store) DismissKey(key string) error {
	return s.update(func(list *[]Notice) {
		*list = slices.DeleteFunc(*list, func(n Notice) bool { return n.Key == key })
	})
}

func (s Store) update(fn func(*[]Notice)) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	return s.write(db, fn)
}
