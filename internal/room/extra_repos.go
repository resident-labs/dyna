package room

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// RepoEntry is one repository a room accepts for local changes.
type RepoEntry struct {
	Path string `json:"path"`
	// Primary marks the room's own repository; it cannot be removed.
	Primary bool `json:"primary,omitempty"`
	// Missing marks a registered path that no longer exists on disk.
	Missing bool `json:"missing,omitempty"`
}

// Repos lists the room's repository first, then each registered extra one.
// A registered repository that left the disk is listed as missing.
func (r Record) Repos() []RepoEntry {
	var out []RepoEntry
	if r.Repo != "" {
		out = append(out, RepoEntry{Path: r.Repo, Primary: true, Missing: pathMissing(r.Repo)})
	}
	for _, path := range r.ExtraRepos {
		out = append(out, RepoEntry{Path: path, Missing: pathMissing(path)})
	}
	return out
}

func pathMissing(path string) bool {
	_, err := os.Stat(path)
	return err != nil
}

// AddRepo registers the git repository that holds path, so the room accepts
// its checkouts for local changes. It stores the repository's main worktree.
func (s *Store) AddRepo(ref, path string) error {
	root, err := RepoRoot(path)
	if err != nil {
		return err
	}
	return s.UpdatePreferences(ref, func(r *Record) error {
		if filepath.Clean(r.Repo) == root || slices.Contains(r.ExtraRepos, root) {
			return fmt.Errorf("room %s already accepts %s", r.Name, root)
		}
		r.ExtraRepos = append(r.ExtraRepos, root)
		return nil
	})
}

// RemoveRepo unregisters an extra repository by path. A path that left the
// disk still matches its stored text, so a missing repository can be removed.
func (s *Store) RemoveRepo(ref, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if root, err := RepoRoot(abs); err == nil {
		abs = root
	}
	return s.UpdatePreferences(ref, func(r *Record) error {
		i := slices.Index(r.ExtraRepos, abs)
		if i < 0 {
			return fmt.Errorf("room %s has no registered repository %s", r.Name, abs)
		}
		r.ExtraRepos = slices.Delete(slices.Clone(r.ExtraRepos), i, i+1)
		return nil
	})
}
