package maintenance

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/procident"
)

// Test binaries leave temporary folders in the host temp folder: a 16 GB tmpfs
// on many hosts, which fills after a few runs of the suite. A test binary
// removes the folders of earlier runs (internal/testsafe). This sweep frees the
// folders that no later run reaches, which older Dyna builds left behind.
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
		if !entry.IsDir() || !strings.HasPrefix(name, testFolderPrefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !staleTestFolder(name, info.ModTime(), now, alive) {
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
