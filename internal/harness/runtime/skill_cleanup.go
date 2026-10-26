package runtime

import (
	"errors"
	"os"
	"path/filepath"
)

// removeLegacySkillInstalls deletes only the skill material that older Dyna
// versions wrote into harness homes:
//
//   - the legacy whole-view symlink at a home's "skills" directory;
//   - links inside a home's "skills" directory that point into Dyna's
//     generations directory;
//   - the seeded load-dyna-orchestrator directory beside harness-config.json.
//
// The ".runtime" home of each supported harness is one of the homes listed
// under the accounts root, so its skills view is covered too. A real directory,
// a real file or a link outside the generations directory belongs to the owner
// or the harness and stays. The cleanup never follows a symlink out of the
// account tree, and it repeats safely until an old home is gone.
func (r *Runtime) removeLegacySkillInstalls() error {
	if r.Config != nil && r.Config.Path != "" {
		seeded := filepath.Join(filepath.Dir(r.Config.Path), "skills", "load-dyna-orchestrator")
		if err := os.RemoveAll(seeded); err != nil {
			return err
		}
	}
	if r.Accounts == nil || r.Accounts.Root == "" || safeAccountPath(r.Accounts.Root) != nil {
		return nil
	}
	harnesses, err := os.ReadDir(r.Accounts.Root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, harness := range harnesses {
		// A symlinked harness directory is not Dyna's account tree; do not follow it.
		if !harness.IsDir() {
			continue
		}
		root := filepath.Join(r.Accounts.Root, harness.Name())
		homes, err := os.ReadDir(root)
		if err != nil {
			return err
		}
		for _, home := range homes {
			if !home.IsDir() {
				continue
			}
			if err := r.removeOwnedSkillView(filepath.Join(root, home.Name(), "skills")); err != nil {
				return err
			}
		}
	}
	return nil
}

// removeOwnedSkillView removes Dyna-owned links in one home's skills view. It
// keeps a view that is not a link or a directory, and it never descends into a
// link.
func (r *Runtime) removeOwnedSkillView(view string) error {
	info, err := os.Lstat(view)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if !r.ownedSkillLink(view) {
			return nil
		}
		return os.Remove(view)
	}
	if !info.IsDir() {
		return nil
	}
	entries, err := os.ReadDir(view)
	if err != nil {
		return err
	}
	var failures []error
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		link := filepath.Join(view, entry.Name())
		if !r.ownedSkillLink(link) {
			continue
		}
		if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// ownedSkillLink reports whether link points into Dyna's generations directory.
// A relative target or a data-root alias resolves the same way a generation pin
// does. Readlink reads the link itself, so this never follows it.
func (r *Runtime) ownedSkillLink(link string) bool {
	if r.Generations == "" {
		return false
	}
	target, err := os.Readlink(link)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(link), target)
	}
	target = filepath.Clean(target)
	if canonical, err := canonicalAccountDataRoot(target); err == nil {
		target = canonical
	}
	return inside(r.Generations, target)
}
