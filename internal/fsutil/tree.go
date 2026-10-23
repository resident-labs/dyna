package fsutil

import (
	"io/fs"
	"os"
	"path/filepath"
)

// RemoveTree removes a folder tree that may be read only, such as a frozen
// harness bundle. Removing an entry needs write permission on its folder, so
// the walk restores that first. RemoveTree ignores a folder it cannot read and
// returns the removal error.
func RemoveTree(path string) error {
	_ = filepath.WalkDir(path, func(p string, entry fs.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			_ = os.Chmod(p, 0o700)
		}
		return nil
	})
	return os.RemoveAll(path)
}
