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

// currentImageLink is the file name a published link carries.
const currentImageLink = "current"

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

// CurrentBuild returns the image of the build that source names, for a running
// image such as /proc/<pid>/exe and for a copy in the image cache. It reports
// false for a path that survives an update by itself, such as an installed
// binary that the update replaces in place.
func CurrentBuild(source string) (string, bool, error) {
	if runtime.GOOS != "linux" {
		return "", false, nil
	}
	if strings.HasPrefix(source, "/proc/") {
		image, err := StableImage(source)
		return image, err == nil, err
	}
	service, err := cacheRoot()
	if err != nil {
		return "", false, err
	}
	if filepath.Dir(source) != filepath.Join(service, "bin") || !imageCopyName(filepath.Base(source)) {
		return "", false, nil
	}
	return source, true, nil
}

// CurrentLink returns the path one native scope publishes to name the build the
// engine runs now. Its own directory keeps pruning from treating the link as an
// image, and a scope that is not the engine's still gets a link of its own.
func CurrentLink(scope string) (string, error) {
	service, err := cacheRoot()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(scope))
	return filepath.Join(service, "links", hex.EncodeToString(sum[:8]), currentImageLink), nil
}

// SetLink makes link name target. Each update stages the link under a name of
// its own and renames it into place, so callers that publish at the same time
// never share a staging path, and a command that execs the link runs either the
// old target or the new one, never a path that does not exist.
func SetLink(link, target string) error {
	if current, err := os.Readlink(link); err == nil && current == target {
		return nil
	}
	dir := filepath.Dir(link)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(dir, ".link-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	staged := filepath.Join(stage, currentImageLink)
	if err := os.Symlink(target, staged); err != nil {
		return err
	}
	return os.Rename(staged, link)
}

// linkRoot is the directory that holds one link directory per native scope.
func linkRoot() (string, error) {
	service, err := cacheRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(service, "links"), nil
}

// linkedImages names every image a published link still points at. Pruning
// keeps them: a scope that has not run a turn since an update still names the
// build it last published, and deleting that image would break its wrappers.
func linkedImages() map[string]bool {
	out := map[string]bool{}
	root, err := linkRoot()
	if err != nil {
		return out
	}
	scopes, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	for _, scope := range scopes {
		if !scope.IsDir() {
			continue
		}
		if target, err := os.Readlink(filepath.Join(root, scope.Name(), currentImageLink)); err == nil {
			out[target] = true
		}
	}
	return out
}

// cacheRoot is the directory that holds copied images and published links.
func cacheRoot() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "dyna"), nil
}

// imageCacheDir is where copied images live.
func imageCacheDir() (string, error) {
	root, err := cacheRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "bin"), nil
}

// imageCopyName reports whether name is one copied image.
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

// pruneStableExecutables keeps the newest copies, the one just written and
// every copy a published link names. It never removes a copy that a running
// process executes: a long-lived session MCP server or an idle harness child
// still runs the image its turn named, and deleting it would break the next
// launch of that same path.
func pruneStableExecutables(dir, keep string) {
	inUse := runningImages()
	for image := range linkedImages() {
		inUse[image] = true
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
