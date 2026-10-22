// Package home prepares private discovery roots without hiding owner toolchains.
// It is a configuration boundary, not a filesystem sandbox.
package home

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var xdgNames = []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"}

// Home returns the stable HOME paired with a native credential scope.
func Home(nativeScope string) string { return nativeScope + ".home" }

// Roots returns HOME and the four private XDG settings roots.
func Roots(nativeScope string) map[string]string {
	h := Home(nativeScope)
	return map[string]string{"HOME": h, "XDG_CONFIG_HOME": filepath.Join(h, ".config"), "XDG_DATA_HOME": filepath.Join(h, ".local/share"), "XDG_CACHE_HOME": filepath.Join(h, ".cache"), "XDG_STATE_HOME": filepath.Join(h, ".local/state")}
}

// Ensure creates private roots. It checks only their ancestors, never their contents
// or the paired native credential tree. Existing root modes are repaired to 0700.
func Ensure(nativeScope string) error {
	if !filepath.IsAbs(nativeScope) {
		return fmt.Errorf("native scope must be absolute")
	}
	for _, path := range Roots(nativeScope) {
		if err := privateDir(path); err != nil {
			return err
		}
	}
	return nil
}

func privateDir(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(abs, current), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err = os.Mkdir(current, 0700); err != nil && !os.IsExist(err) {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe root ancestor %q: expected directory without symlink", current)
		}
	}
	return os.Chmod(abs, 0700)
}

// atomicFile never follows a destination symlink or writes through a hard link.
func atomicFile(path string, data []byte, mode os.FileMode) error {
	if err := privateDir(filepath.Dir(path)); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsafe generated file %q: expected regular file", path)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".harnesshome-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
