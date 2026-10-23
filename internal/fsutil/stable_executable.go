package fsutil

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// stableExecutableCopies is how many running images the cache keeps. A copied
// image is shared by every worker of one build; older builds age out once a
// few newer ones exist.
const stableExecutableCopies = 4

// StableExecutable returns a path to the binary this process runs that outlives
// the process. Harness MCP commands, $DYNA_BIN and their published tool homes
// keep this path across turns, engine restarts and installed updates, so a
// later worker can still start. Linux copies the running image from /proc
// instead of naming it: the copy is byte-for-byte the build this process runs,
// which keeps a worker on its engine's version even while an installed update
// waits for a restart. Naming /proc/<pid>/exe would break as soon as this
// process exits.
func StableExecutable() (string, error) {
	if runtime.GOOS != "linux" {
		return os.Executable()
	}
	source := fmt.Sprintf("/proc/%d/exe", os.Getpid())
	info, err := os.Stat(source)
	if err != nil {
		return "", err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, "dyna", "bin")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	// One image identity, one file: every worker, turn and restart of the same
	// build reuses the first copy.
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%d\x00%s", info.Size(), info.ModTime().UnixNano(), info.Mode().String())))
	path := filepath.Join(dir, "dyna-"+hex.EncodeToString(sum[:8]))
	if existing, err := os.Stat(path); err == nil && existing.Size() == info.Size() {
		return path, nil
	}
	if err := copyExecutable(source, path, dir); err != nil {
		return "", err
	}
	pruneStableExecutables(dir, filepath.Base(path))
	return path, nil
}

// copyExecutable writes a private copy of source under dir and renames it into
// place, so readers never see a partial image.
func copyExecutable(source, path, dir string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	temp, err := os.CreateTemp(dir, ".dyna-image-")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0700); err != nil {
		temp.Close()
		return err
	}
	if _, err := io.Copy(temp, in); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

// pruneStableExecutables keeps the newest copies and the one just written.
func pruneStableExecutables(dir, keep string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type copyFile struct {
		name  string
		mtime int64
	}
	copies := make([]copyFile, 0, len(entries))
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "dyna-") || entry.Name() == keep {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		copies = append(copies, copyFile{entry.Name(), info.ModTime().UnixNano()})
	}
	if len(copies) < stableExecutableCopies {
		return
	}
	sort.Slice(copies, func(i, j int) bool { return copies[i].mtime > copies[j].mtime })
	for _, old := range copies[stableExecutableCopies-1:] {
		_ = os.Remove(filepath.Join(dir, old.name))
	}
}
