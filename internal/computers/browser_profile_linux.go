//go:build linux

package computers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"dyna-agent/internal/browserconfig"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store"
	"golang.org/x/sys/unix"
)

const smallBrowserStorage = 16 << 20

func browserMaster() string             { return filepath.Join(filepath.Dir(Root()), "browser", "profile") }
func browserLeasePath(id string) string { return filepath.Join(Dir(id), "browser-profile.json") }

type browserLease struct {
	Mode         string            `json:"mode"`
	OwnerProfile string            `json:"ownerProfile"`
	SaveBack     bool              `json:"saveBack"`
	Baseline     map[string]string `json:"baseline,omitempty"`
}

type chromiumLaunch struct {
	Executable string   `json:"executable"`
	Args       []string `json:"args"`
}

// withBrowserMaster serializes both snapshots and save-back. Chromium never
// runs on this directory. Keep the lock outside the replaceable profile tree.
func withBrowserMaster(ctx context.Context, fn func() error) error {
	path := filepath.Join(filepath.Dir(browserMaster()), "profile.lock")
	f, err := browserFileLock(ctx, path, false)
	if err != nil {
		return err
	}
	defer f.Close()
	// Recover an interrupted atomic directory replacement before reading master.
	if _, err := os.Stat(browserMaster()); os.IsNotExist(err) {
		if err := os.Rename(browserMaster()+".previous", browserMaster()); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return fn()
}

func browserFileLock(ctx context.Context, path string, nonblocking bool) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if nonblocking || (!errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN)) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// Only these login state paths enter snapshots. Database sidecars are needed
// when copying an owner's live SQLite databases. Chromium rebuilds shared memory.
var browserLoginFiles = []string{"Cookies", "Network/Cookies", "Login Data", "Web Data"}
var browserStorageDirs = []string{"Local Storage", "IndexedDB"}

func copyBrowserProfile(ctx context.Context, src, srcProfile, dst string, storage bool) error {
	if err := os.MkdirAll(filepath.Join(dst, "Default"), 0700); err != nil {
		return err
	}
	if err := copyBrowserFile(filepath.Join(src, "Local State"), filepath.Join(dst, "Local State")); err != nil {
		return err
	}
	for _, name := range append([]string{"Preferences"}, browserLoginFiles...) {
		from, to := filepath.Join(src, srcProfile, name), filepath.Join(dst, "Default", name)
		if !storage && name != "Preferences" {
			if err := snapshotBrowserDatabase(ctx, from, to); err != nil {
				return err
			}
			continue
		}
		if err := copyBrowserFile(from, to); err != nil {
			return err
		}
		if name != "Preferences" {
			if err := copyBrowserFile(filepath.Join(src, srcProfile, name+"-wal"), filepath.Join(dst, "Default", name+"-wal")); err != nil {
				return err
			}
		}
	}
	if storage {
		for _, name := range browserStorageDirs {
			from := filepath.Join(src, srcProfile, name)
			size, err := browserTreeSize(from)
			if err != nil {
				return err
			}
			if size > smallBrowserStorage {
				continue
			}
			if err := copyBrowserTree(from, filepath.Join(dst, "Default", name)); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyBrowserFile(src, dst string) error {
	info, err := os.Lstat(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("browser login file is not a regular file: %s", src)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	closeErr := out.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func browserTreeSize(root string) (int64, error) {
	var size int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if os.IsNotExist(err) && path == root {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("browser storage contains a symlink: %s", path)
		}
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("browser storage is not a regular file: %s", path)
			}
			size += info.Size()
		}
		return nil
	})
	return size, err
}

func copyBrowserTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if os.IsNotExist(err) && path == src {
			return nil
		}
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		return copyBrowserFile(path, target)
	})
}

func replaceBrowserProfile(ctx context.Context, src, profile, dst string, storage bool) error {
	return stageBrowserProfile(dst, func(staged string) error { return copyBrowserProfile(ctx, src, profile, staged, storage) })
}

func stageBrowserProfile(dst string, copyState func(string) error) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	staged, err := os.MkdirTemp(filepath.Dir(dst), ".browser-profile-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staged)
	if err := copyState(staged); err != nil {
		return err
	}
	// Both directories are Dyna-owned, and the caller holds the appropriate lock.
	old := dst + ".previous"
	if err := os.RemoveAll(old); err != nil {
		return err
	}
	if err := os.Rename(dst, old); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(staged, dst); err != nil {
		_ = os.Rename(old, dst)
		return err
	}
	return os.RemoveAll(old)
}

func readBrowserLease(id string) (browserLease, error) {
	var lease browserLease
	b, err := os.ReadFile(browserLeasePath(id))
	if err == nil {
		err = json.Unmarshal(b, &lease)
	}
	return lease, err
}

func prepareBrowserProfile(ctx context.Context, id string, v settings.Values) error {
	lease := browserLease{Mode: v.BrowserProfileMode, OwnerProfile: v.BrowserOwnerProfile, SaveBack: v.BrowserSaveBack}
	if lease.Mode == "copy-mine" && !browserconfig.CopyMineAvailable() {
		return fmt.Errorf("copy-mine is unavailable without the owner's keyring in WSL or the managed container")
	}
	if lease.Mode != "separate" && lease.Mode != "shared" && lease.Mode != "copy-mine" {
		return fmt.Errorf("invalid browser profile mode %q", lease.Mode)
	}
	old, err := readBrowserLease(id)
	if err == nil && old.Mode == lease.Mode && old.OwnerProfile == lease.OwnerProfile && old.SaveBack == lease.SaveBack {
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		if err := saveBrowserProfile(ctx, id); err != nil {
			return err
		}
	}
	dst := computerBrowserProfile(id)
	switch lease.Mode {
	case "separate":
		// Preserve the old per-computer profile when upgrading the default.
		if old.Mode != "" && old.Mode != "separate" {
			if err := os.RemoveAll(dst); err != nil {
				return err
			}
		}
		if err := os.MkdirAll(dst, 0700); err != nil {
			return err
		}
	case "shared":
		if err := withBrowserMaster(ctx, func() error {
			return replaceBrowserProfile(ctx, browserMaster(), "Default", dst, true)
		}); err != nil {
			return err
		}
	case "copy-mine":
		if lease.OwnerProfile == "" || lease.OwnerProfile == "." || lease.OwnerProfile == ".." || strings.ContainsAny(lease.OwnerProfile, "/\\") {
			return fmt.Errorf("select a Chromium profile directory")
		}
		src := browserconfig.OwnerRoot()
		if info, err := os.Stat(filepath.Join(src, lease.OwnerProfile, "Preferences")); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("selected Chromium profile has no Preferences file")
		}
		if err := replaceBrowserProfile(ctx, src, lease.OwnerProfile, dst, false); err != nil {
			return err
		}
	}
	if lease.Mode == "shared" {
		lease.Baseline, err = browserProfileHashes(dst)
		if err != nil {
			return err
		}
	}
	return store.WriteJSON(browserLeasePath(id), lease, 0o600)
}

// withBrowserCleanup serializes one computer's profile release across
// processes: the lease read, the save-back and the deletion of its profile.
// A destroy call that expires falls back to the offline path while the daemon
// still cleans up, so both paths take this lock. The second one then finds no
// lease and keeps the state the first one saved.
// browserCleanupWait reports each attempt to take the cleanup lock, so a test
// can tell when a second cleanup path is waiting for it.
var browserCleanupWait = func(string) {}

// browserCleanupLocked reports that this path now holds the cleanup lock, so a
// test can act only after the daemon's own save-back started.
var browserCleanupLocked = func(string) {}

func withBrowserCleanup(ctx context.Context, id string, fn func() error) error {
	browserCleanupWait(id)
	f, err := browserFileLock(ctx, filepath.Join(Dir(id), "cleanup.lock"), false)
	if err != nil {
		return err
	}
	defer f.Close()
	browserCleanupLocked(id)
	return fn()
}

func saveBrowserProfile(ctx context.Context, id string) error {
	lease, err := readBrowserLease(id)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if lease.Mode == "shared" && lease.SaveBack {
		// The caller must stop Chromium before entering this function.
		if len(profileBrowserPIDs(id)) != 0 {
			return fmt.Errorf("Chromium is still running; browser login state was not saved")
		}
		if err := withBrowserMaster(ctx, func() error {
			return mergeBrowserProfile(ctx, computerBrowserProfile(id), lease)
		}); err != nil {
			return err
		}
	}
	return os.Remove(browserLeasePath(id))
}

// browserSaveBackBudget bounds one save-back: the cleanup lock, the master
// lock, and every sqlite3 merge run inside them. A save cut off by the budget
// keeps its lease and profile, so a later cleanup can retry it.
const browserSaveBackBudget = 60 * time.Second

// saveBackBrowserProfile saves login state under the caller's context.
func saveBackBrowserProfile(ctx context.Context, id string) error {
	return withBrowserCleanup(ctx, id, func() error { return saveBrowserProfile(ctx, id) })
}

// SQLite's backup command takes a consistent read-only snapshot, including
// committed WAL data, while the owner's Chromium continues to write.
func snapshotBrowserDatabase(ctx context.Context, src, dst string) error {
	f, err := os.Open(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var header [16]byte
	n, _ := f.Read(header[:])
	f.Close()
	if n != 16 || string(header[:]) != "SQLite format 3\x00" {
		return copyBrowserFile(src, dst)
	}
	if info, err := os.Lstat(src); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("browser database is not a regular file: %s", src)
	}
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		return fmt.Errorf("copy-mine needs sqlite3 to snapshot live Chromium login databases")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	uri := (&url.URL{Scheme: "file", Path: src, RawQuery: "mode=ro"}).String()
	command := ".backup '" + strings.ReplaceAll(dst, "'", "''") + "'"
	out, err := combinedOwned(exec.CommandContext(ctx, sqlite, "-readonly", "-cmd", ".timeout 2000", uri, command))
	if err != nil {
		return fmt.Errorf("snapshot Chromium database: %w: %s", err, out)
	}
	return os.Chmod(dst, 0600)
}
