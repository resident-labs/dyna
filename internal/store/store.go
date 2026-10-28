// Package store provides file locks and atomic JSON documents.
//
// Runtime state that changes and is queried belongs in SQLite (dyna.db), per
// room rule r1. Keep store for settings and config files that stay JSON, and
// for the file locks that guard those files and other shared resources.
//
// Keep each caller's lock path unchanged. Acquire session metadata/gate locks
// before room locks. When both room locks are needed, take the root lock before
// the individual room lock. Never reacquire a held lock or call a room writer
// from a room snapshot callback. Release nested locks in reverse order.
// internal/lockrank enforces this order in tests and debug builds.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"

	"dyna-agent/internal/fsutil"
	// Every test binary that resolves the data folder starts away from the
	// user's live Dyna (testsafe's init).
	_ "dyna-agent/internal/testsafe"
)

// Lock takes an exclusive flock on path, creating the lock file with mode 0600.
// It does not create directories. The caller must call unlock exactly once.
// Separate opens contend even within one process; no process mutex is used.
func Lock(path string) (unlock func(), err error) {
	return LockContext(context.Background(), path)
}

// LockContext is Lock, but stops waiting when ctx ends.
// The caller must call unlock exactly once after a successful return.
func LockContext(ctx context.Context, path string) (unlock func(), err error) {
	unlock, _, err = flock(ctx, path, syscall.LOCK_EX, false)
	return unlock, err
}

// TryLock takes an exclusive flock like Lock, but reports owned=false instead of
// waiting when another open already holds it. The caller must call unlock after
// a true result. It does not create directories.
func TryLock(path string) (unlock func(), owned bool, err error) {
	return flock(context.Background(), path, syscall.LOCK_EX|syscall.LOCK_NB, false)
}

// TryLockAndRemove is a nonblocking lock whose file is removed before unlock.
// Every user of this path must use this function, so an open of an old inode
// cannot enter after another owner removes the file.
func TryLockAndRemove(path string) (unlock func(), owned bool, err error) {
	return flock(context.Background(), path, syscall.LOCK_EX|syscall.LOCK_NB, true)
}

// flock takes the flock with the caller's flags, waiting until ctx ends.
// With LOCK_NB, a held lock reports owned=false and no error.
func flock(ctx context.Context, path string, flags int, remove bool) (unlock func(), owned bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	return flockOpened(ctx, path, f, flags, remove)
}

func flockOpened(ctx context.Context, path string, f *os.File, flags int, remove bool) (unlock func(), owned bool, err error) {
	if flags&syscall.LOCK_NB == 0 {
		err = FlockContext(ctx, f)
	} else {
		err = syscall.Flock(int(f.Fd()), flags)
	}
	if err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if remove {
		held, heldErr := f.Stat()
		named, namedErr := os.Stat(path)
		if heldErr != nil || namedErr != nil || !os.SameFile(held, named) {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			_ = f.Close()
			if os.IsNotExist(namedErr) {
				namedErr = nil
			}
			return nil, false, errors.Join(heldErr, namedErr)
		}
	}
	return func() {
		if remove {
			_ = os.Remove(path)
		}
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true, nil
}

// ReadJSON decodes path into v. A missing file leaves v unchanged and returns
// no error; callers should pass a pointer to a zero value for an empty document.
func ReadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// WriteJSON atomically replaces path with two-space indented JSON and a final
// newline. A json.RawMessage is an already encoded document: its bytes are
// written unchanged so callers with other existing formats can preserve them.
// It sets mode explicitly and does not create directories or sync the file.
func WriteJSON(path string, v any, mode os.FileMode) error {
	b, ok := v.(json.RawMessage)
	if ok {
		// Validate without changing the caller's whitespace or field order.
		encoded, err := json.Marshal(b)
		if err != nil {
			return err
		}
		if b == nil {
			b = encoded // A nil RawMessage represents JSON null.
		}
	} else {
		encoded, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		b = append(encoded, '\n')
	}
	return fsutil.WriteFile(path, b, mode, false)
}

// Update locks lockPath, reads path into v, calls fn, then writes v with mode
// 0600. v must point to a zero value; fn can close over v. An error leaves
// the file unchanged. The caller must create directories first and must not
// acquire lockPath again in fn.
func Update(lockPath, path string, v any, fn func() error) error {
	unlock, err := Lock(lockPath)
	if err != nil {
		return err
	}
	defer unlock()
	if err := ReadJSON(path, v); err != nil {
		return err
	}
	if err := fn(); err != nil {
		return err
	}
	return WriteJSON(path, v, 0o600)
}

// ConfigDir uses the same XDG configuration lookup and HOME fallback as profile.
func ConfigDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "dyna")
}

// DataDir is the one place that resolves Dyna's data folder:
// $XDG_DATA_HOME/dyna, else ~/.local/share/dyna. It panics when that folder
// is inside a harness runtime home (see CheckDataDir). The dyna command checks
// first and exits with the error, so the panic only stops a test or an
// entry point that skipped the check.
func DataDir() string {
	if err := CheckDataDir(); err != nil {
		panic(err)
	}
	return dataDir()
}

// RuntimeHomeName is the private HOME of a harness runtime,
// accounts/<harness>/.runtime.home. Its XDG roots belong to the harness.
const RuntimeHomeName = ".runtime.home"

// CheckDataDir refuses a data folder inside a harness runtime home. A process
// gets one when it runs inside a harness without the owner's environment: a
// Go test that keeps the private XDG_DATA_HOME, or a dyna command started with
// DYNA_OWNER_ENV unset. Writing there would start a second, partial store.
func CheckDataDir() error {
	dir := dataDir()
	for _, part := range strings.Split(filepath.Clean(dir), string(filepath.Separator)) {
		if part == RuntimeHomeName {
			return fmt.Errorf("dyna data folder %s is inside a harness runtime home; this process lost the owner's environment. Keep DYNA_OWNER_ENV set when you run dyna in a harness, and give tests a temporary XDG_DATA_HOME (storetest.DataDir)", dir)
		}
	}
	return nil
}

// DataDirEnv names the data folder directly. A dyna client command started
// with its own XDG roots gets it, so it still finds the owner's engine (see
// home.RestoreOwner). It wins over XDG_DATA_HOME and HOME.
const DataDirEnv = "DYNA_DATA_DIR"

func dataDir() string {
	if dir := os.Getenv(DataDirEnv); filepath.IsAbs(dir) {
		return dir
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "dyna")
	}
	return DataDirForHome(os.Getenv("HOME"))
}

// OwnerDataDir is the default data folder of the user's own home (from the
// password database, not $HOME), or "" when that home is unknown.
func OwnerDataDir() string {
	u, err := user.Current()
	if err != nil || u.HomeDir == "" {
		return ""
	}
	return DataDirForHome(u.HomeDir)
}

// DataDirForHome returns the data folder for a home without an XDG override.
func DataDirForHome(home string) string {
	return filepath.Join(home, ".local", "share", "dyna")
}
