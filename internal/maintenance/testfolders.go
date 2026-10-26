package maintenance

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/procident"
)

// Test binaries leave temporary folders in the host temp folder: a 16 GB tmpfs
// on many hosts, which fills after a few runs of the suite and then breaks
// self-update and later tests. A test binary removes the folders of earlier runs
// (internal/testsafe). This sweep frees the folders that no later run reaches.
//
// Only names that this package makes are removed. A generic pattern such as
// "Test" or "tmp." also matches an unrelated program's workspace, and a folder
// that is still in use is not old in any useful sense: writing a file inside it
// does not touch the folder's own time.
const testFolderPrefix = "dyna-test-"

// testFolderRetention is how long a test folder must stand before the sweep
// removes it. The folder of a test binary that has ended may go sooner, but a
// later run removes that one (internal/testsafe), and a process ID can come
// back.
const testFolderRetention = 6 * time.Hour

// sweepTestFolders removes the stale test folders in root. A folder that holds
// a frozen harness bundle is read only, so the sweep restores write permission
// on its folders before removal.
func sweepTestFolders(now time.Time, root string, alive func(int) bool, dry bool) (Pruned, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return Pruned{}, nil
	}
	if err != nil {
		return Pruned{}, err
	}
	var freed Pruned
	var errs []error
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || !testFolderName(name) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !staleTestFolder(name, info.ModTime(), now, alive) || !ownedByUser(info) {
			continue
		}
		path := filepath.Join(root, name)
		freed.Items++
		freed.Bytes += folderBytes(path)
		if dry {
			continue
		}
		if err := fsutil.RemoveTree(path); err != nil {
			errs = append(errs, err)
		}
	}
	return freed, errors.Join(errs...)
}

// processAlive reports whether a process still exists. The sweep takes it as a
// parameter so a test can name the running binaries itself.
var processAlive = procident.Alive

// testFolderName reports whether name is a temporary folder of a test binary.
// The host temp folder also holds other users' files, the owner's own folders
// and a process guard's control folder, so only this name is ever considered.
func testFolderName(name string) bool {
	if strings.HasPrefix(name, testFolderPrefix) {
		return true
	}
	for _, prefix := range leakedTempPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// leakedTempPrefixes names temporary folders that Dyna code and its tests make
// with no process ID. A killed test run or tool leaves them behind, and the
// retention rule is the only check they get. A name that a live process guards
// with a socket (dyna-pg-, dyna-engine-) is not on the list.
var leakedTempPrefixes = []string{
	"dyna-go-env-",
	"dyna-pr-detail-",
	"dyna-conversation-",
	"dyna-checkout-template-",
	"dyna-health-tests-",
}

// ownedByUser reports whether this user owns the folder. The sweep never removes
// a folder that belongs to someone else, whatever its name.
func ownedByUser(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}

// staleTestFolder reports whether a test folder outlived its test binary.
func staleTestFolder(name string, modified, now time.Time, alive func(int) bool) bool {
	pid, ok := testFolderPID(name)
	return now.Sub(modified) > testFolderRetention && (!ok || !alive(pid))
}

// testFolderPID reads the ID of the test binary that made a folder. Older Dyna
// builds gave the folder no ID.
func testFolderPID(name string) (int, bool) {
	rest, ok := strings.CutPrefix(name, testFolderPrefix)
	if !ok {
		return 0, false
	}
	digits, _, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, false
	}
	pid, err := strconv.Atoi(digits)
	return pid, err == nil && pid > 0
}

// folderBytes sums the file sizes under path. It counts no folder itself.
func folderBytes(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if info, err := entry.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
