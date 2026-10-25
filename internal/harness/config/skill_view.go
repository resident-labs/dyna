package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const skillLinkPrefix = ".dyna-link-"

// PublishSkillView links each skill in a generation's skills directory into
// view. The view is a real directory, so a harness that writes its own skills
// there never changes the read-only generation. Dyna owns
// only the symlinks in view. Each link is replaced with an atomic rename, and
// links to skills that the generation no longer has are removed.
func PublishSkillView(view, skills string) error {
	info, err := os.Lstat(view)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		// TODO: legacy/shim remove later — older builds linked the whole view to one generation; remove once every account home has republished its skills.
		if err = os.Remove(view); err != nil {
			return err
		}
	case err == nil && !info.IsDir():
		return errors.New("native skills view must be a directory")
	case err != nil && !os.IsNotExist(err):
		return err
	}
	if err = os.MkdirAll(view, 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(skills)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	want := map[string]bool{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		want[entry.Name()] = true
		link := filepath.Join(view, entry.Name())
		if info, err := os.Lstat(link); err == nil && info.Mode()&os.ModeSymlink == 0 {
			continue // The harness or the user owns this name.
		}
		tmp, err := os.CreateTemp(view, skillLinkPrefix)
		if err != nil {
			return err
		}
		name := tmp.Name()
		tmp.Close()
		os.Remove(name)
		if err = os.Symlink(filepath.Join(skills, entry.Name()), name); err != nil {
			return err
		}
		if err = os.Rename(name, link); err != nil {
			os.Remove(name)
			return err
		}
	}
	current, err := os.ReadDir(view)
	if err != nil {
		return err
	}
	for _, entry := range current {
		// A concurrent publisher can own a temporary link that is not renamed yet.
		if entry.Type()&os.ModeSymlink != 0 && !want[entry.Name()] && !strings.HasPrefix(entry.Name(), skillLinkPrefix) {
			if err = os.Remove(filepath.Join(view, entry.Name())); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}
