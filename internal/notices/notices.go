// Package notices stores machine-wide owner notices for the Dyna home screen.
package notices

import (
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"slices"
	"time"

	"dyna-agent/internal/store"
)

const KindSync = "sync"

// Notice is one owner-facing alert. Kind is "disk", "hook", "provider" or "sync".
// Severity is "error" or "warn". Command is an optional shell command the
// owner can copy.
type Notice struct {
	ID       string    `json:"id"`
	Key      string    `json:"key"`
	Kind     string    `json:"kind"`
	Severity string    `json:"severity"`
	Text     string    `json:"text"`
	Command  string    `json:"command,omitempty"`
	At       time.Time `json:"at"`
}

// Store keeps machine-wide notices in dyna.db under Dir.
type Store struct{ Dir string }

func DefaultStore() Store { return Store{Dir: store.DataDir()} }

// Path is the previous release's notice copy. Watchers use Revision.
func (s Store) Path() string { return filepath.Join(s.Dir, "notices.json") }

// Post adds n. A notice with the same Key is replaced in place, so each key
// shows once with its newest text and keeps its ID.
func (s Store) Post(n Notice) (Notice, error) {
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

func (s Store) update(fn func(*[]Notice)) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	return s.write(db, fn)
}
