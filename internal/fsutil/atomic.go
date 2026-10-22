// Package fsutil provides shared file persistence helpers.
package fsutil

import (
	"os"
	"path/filepath"
	"syscall"
)

// WriteFile replaces path by renaming a temporary file in the same directory.
// It sets mode explicitly. If sync is true, it syncs the file before rename.
// It does not create the parent directory or sync the directory after rename;
// call SyncDir for that.
func WriteFile(path string, data []byte, mode os.FileMode, sync bool) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if sync {
		if err := tmp.Sync(); err != nil {
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// WriteExecutable replaces path with an executable that can run immediately.
// A concurrent fork can inherit an open writer until the child calls exec,
// causing ETXTBSY even after this process closes the file. Hold ForkLock until
// WriteFile closes the writer so os/exec cannot fork while it is open.
func WriteExecutable(path string, data []byte, mode os.FileMode) error {
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	return WriteFile(path, data, mode, false)
}
