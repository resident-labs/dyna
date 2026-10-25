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

// stableImageCopies is how many running images the cache keeps beside the ones
// a live process runs. A copied image is shared by every worker of one build;
// older builds age out once newer ones exist.
const stableImageCopies = 4

// currentImageLink always names the image of the build the engine runs now.
// Published wrappers exec this link, so one engine update moves them all.
const currentImageLink = "dyna-current"

// StableExecutable returns a path to the binary this process runs that outlives
// the process. Harness MCP commands, $DYNA_BIN and their published tool homes
// keep this path across turns, engine restarts and installed updates, so a
// later worker can still start. Linux copies the running image from /proc
// instead of naming it: the copy is byte-for-byte the build this process runs,
// which keeps a worker on its engine's version even while an installed update
// waits for a restart. Naming /proc/<pid>/exe would break as soon as this
// process exits. Publish CurrentImage of this path instead of the path itself: a
// wrapper that names the copy stays on this build forever.
func StableExecutable() (string, error) { return StableImage(fmt.Sprintf("/proc/%d/exe", os.Getpid())) }

// StableImage returns a stable path holding the image at source. It covers
// every pinned image path, including another process's /proc/<pid>/exe, so a
// harness MCP server named by such a path still starts on a later turn.
func StableImage(source string) (string, error) {
	if runtime.GOOS != "linux" {
		return os.Executable()
	}
	info, err := os.Stat(source)
	if err != nil {
		return "", err
	}
	dir, err := imageCacheDir()
	if err != nil {
		return "", err
	}
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

// CurrentImage returns a path that always names the image of one build: the
// path is a link in the image cache, and StableImage moves the link whenever
// the build changes. Callers publish the link instead of one build's copy, so
// a wrapper, a harness MCP command or $DYNA_BIN that outlives an engine update
// calls the current build rather than the build it first named.
//
// source is a running image such as /proc/<pid>/exe or a copy in the image
// cache. Any other path, such as an installed binary that an update replaces
// in place, is returned unchanged.
func CurrentImage(source string) (string, error) {
	if runtime.GOOS != "linux" {
		return os.Executable()
	}
	if strings.HasPrefix(source, "/proc/") {
		image, err := StableImage(source)
		if err != nil {
			return "", err
		}
		return pointCurrentImage(filepath.Dir(image), image)
	}
	dir, err := imageCacheDir()
	if err != nil {
		return "", err
	}
	if filepath.Dir(source) != dir || !imageCopyName(filepath.Base(source)) {
		return source, nil
	}
	return pointCurrentImage(dir, source)
}

// pointCurrentImage makes the current link name image. The link is replaced by
// rename, so a command that execs it during an update runs either the old build
// or the new one, never a path that does not exist.
func pointCurrentImage(dir, image string) (string, error) {
	link := filepath.Join(dir, currentImageLink)
	if target, err := os.Readlink(link); err == nil && target == image {
		return link, nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	staged := filepath.Join(dir, "."+currentImageLink+".new")
	if err := os.Remove(staged); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Symlink(image, staged); err != nil {
		return "", err
	}
	if err := os.Rename(staged, link); err != nil {
		_ = os.Remove(staged)
		return "", err
	}
	return link, nil
}

// imageCacheDir is where copied images and the current link live.
func imageCacheDir() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "dyna", "bin"), nil
}

// imageCopyName reports whether name is one copied image, never the link.
func imageCopyName(name string) bool {
	const prefix, digits = "dyna-", 16
	if !strings.HasPrefix(name, prefix) || len(name) != len(prefix)+digits {
		return false
	}
	for _, r := range name[len(prefix):] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
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

// pruneStableExecutables keeps the newest copies, the one just written and the
// copy the current link names. It never removes a copy that a running process
// executes: a long-lived session MCP server or an idle harness child still runs
// the image its turn named, and deleting it would break the next launch of that
// same path. It never removes the current build either, because every published
// wrapper execs that copy until the next update moves the link.
func pruneStableExecutables(dir, keep string) {
	inUse := runningImages()
	if current, err := os.Readlink(filepath.Join(dir, currentImageLink)); err == nil {
		inUse[current] = true
	}
	pruneImages(dir, keep, inUse)
}

// pruneImages keeps the newest copies and the one just written, and never
// removes a copy whose path inUse names.
func pruneImages(dir, keep string, inUse map[string]bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	running := inUse
	type copyFile struct {
		name  string
		mtime int64
	}
	copies := make([]copyFile, 0, len(entries))
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "dyna-") || entry.Name() == keep {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if running[path] {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		copies = append(copies, copyFile{entry.Name(), info.ModTime().UnixNano()})
	}
	if len(copies) < stableImageCopies {
		return
	}
	sort.Slice(copies, func(i, j int) bool { return copies[i].mtime > copies[j].mtime })
	for _, old := range copies[stableImageCopies-1:] {
		_ = os.Remove(filepath.Join(dir, old.name))
	}
}

// runningImages names the images live processes execute.
func runningImages() map[string]bool {
	out := map[string]bool{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return out
	}
	for _, entry := range entries {
		if entry.Name()[0] < '0' || entry.Name()[0] > '9' {
			continue
		}
		target, err := os.Readlink(filepath.Join("/proc", entry.Name(), "exe"))
		if err != nil {
			continue
		}
		out[strings.TrimSuffix(target, " (deleted)")] = true
	}
	return out
}
