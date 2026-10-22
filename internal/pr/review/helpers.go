// Package review holds reviewer crew policy.
package review

import (
	"dyna-agent/internal/profile"
	"fmt"
)

// Session is the stored hierarchy and profile data used by reviewer policy.
type Session struct {
	Role          string
	Profile       string
	ParentSession string
}

// Hierarchy reads session data while the caller holds its hierarchy lock.
type Hierarchy interface {
	ReadMeta(id string) (Session, error)
	LiveSessions() ([]Session, error)
}

// CheckReviewerHelper runs under the hierarchy lock, so simultaneous starts
// cannot exceed the crew limit. Stored ancestry remains authoritative.
func CheckReviewerHelper(store Hierarchy, parent string, candidate profile.Profile, profiles *profile.Store) error {
	if parent == "" {
		return nil
	}
	id := parent
	seen := map[string]bool{}
	for id != "" {
		if seen[id] {
			return fmt.Errorf("reviewer helper ancestry contains a cycle")
		}
		seen[id] = true
		meta, err := store.ReadMeta(id)
		if err != nil {
			return err
		}
		if meta.Role == "reviewer" {
			if id != parent {
				return fmt.Errorf("reviewer helpers cannot start helpers; the reviewer crew is one level deep")
			}
			reviewer, ok := profiles.Get(meta.Profile)
			if !ok {
				return fmt.Errorf("reviewer profile %q is unavailable", meta.Profile)
			}
			if candidate.Name != reviewer.Name && candidate.Cost < reviewer.Cost {
				return fmt.Errorf("reviewer helpers must use %s or a cheaper profile (cost-efficiency at least %d; %s has %d)", reviewer.Name, reviewer.Cost, candidate.Name, candidate.Cost)
			}
			all, err := store.LiveSessions()
			if err != nil {
				return err
			}
			count := 0
			for _, child := range all {
				if child.ParentSession == id {
					count++
				}
			}
			if count >= 3 {
				return fmt.Errorf("reviewer already has 3 live helpers; close a helper before starting another")
			}
			return nil
		}
		id = meta.ParentSession
	}
	return nil
}
