package maintenance

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The sweep removes the test folders that outlived their test binary, including
// a read-only harness bundle, and keeps the folders of a running binary.
func TestSweepTestFoldersRemovesOnlyStaleOnes(t *testing.T) {
	root := t.TempDir()
	stale := testFolder(t, root, testFolderPrefix+"4242-1", 2*testFolderRetention, true)
	running := testFolder(t, root, testFolderPrefix+"4343-2", 2*testFolderRetention, false)
	young := testFolder(t, root, testFolderPrefix+"4444-3", 0, false)
	legacy := testFolder(t, root, testFolderPrefix+"999", 2*testFolderRetention, false)
	legacyYoung := testFolder(t, root, testFolderPrefix+"888", 0, false)
	other := testFolder(t, root, "build-cache", 2*testFolderRetention, false)
	alive := func(pid int) bool { return pid == 4343 }

	now := time.Now()
	if freed, err := sweepTestFolders(now, root, alive, true); err != nil || freed.Items != 2 || freed.Bytes == 0 {
		t.Fatalf("dry run = %+v, %v", freed, err)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("a dry run removed a folder: %v", err)
	}

	freed, err := sweepTestFolders(now, root, alive, false)
	if err != nil || freed.Items != 2 || freed.Bytes == 0 {
		t.Fatalf("sweep = %+v, %v", freed, err)
	}
	for _, gone := range []string{stale, legacy} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("a stale test folder stayed: %v", err)
		}
	}
	for _, kept := range []string{running, young, legacyYoung, other} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("a folder that must stay was removed: %v", err)
		}
	}
}

// A killed tool or test run leaves folders that carry no process ID. The sweep
// removes them once they are old, and keeps a young one.
func TestSweepRemovesOldLeakedToolFolders(t *testing.T) {
	root := t.TempDir()
	old := testFolder(t, root, "dyna-go-env-123456", 2*testFolderRetention, false)
	oldBundle := testFolder(t, root, "dyna-pr-detail-AbC123", 2*testFolderRetention, false)
	young := testFolder(t, root, "dyna-go-env-654321", 0, false)

	freed, err := sweepTestFolders(time.Now(), root, func(int) bool { return false }, false)
	if err != nil || freed.Items != 2 {
		t.Fatalf("sweep = %+v, %v; want the two old folders", freed, err)
	}
	for _, gone := range []string{old, oldBundle} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("an old leaked folder stayed: %v", err)
		}
	}
	if _, err := os.Stat(young); err != nil {
		t.Fatalf("a young folder was removed: %v", err)
	}
}

// The disk temp folders are Dyna's own, so an old entry goes whatever its name,
// and a young one stays.
func TestSweepDiskTempRemovesOldEntriesOfAnyName(t *testing.T) {
	root := t.TempDir()
	old := testFolder(t, root, "TestSomething123456", 2*diskTempRetention, false)
	ageTree(t, old, 2*diskTempRetention)
	young := testFolder(t, root, "TestSomething654321", 0, false)

	// A long-running hook writes inside its folder, which leaves the folder's
	// own time old. The sweep must keep it.
	busy := testFolder(t, root, "busy", 2*diskTempRetention, false)
	ageTree(t, busy, 2*diskTempRetention)
	if err := os.WriteFile(filepath.Join(busy, "work.log"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old0 := time.Now().Add(-2 * diskTempRetention)
	if err := os.Chtimes(busy, old0, old0); err != nil {
		t.Fatal(err)
	}

	// A session's folder name is a hash. A live socket server there changes no
	// file time, so the sweep leaves it to the scratch sweep.
	session := testFolder(t, root, "0a1b2c3d", 2*diskTempRetention, false)
	ageTree(t, session, 2*diskTempRetention)

	freed, err := sweepDiskTemp(time.Now(), root, false)
	if _, statErr := os.Stat(session); statErr != nil {
		t.Fatalf("the sweep removed a session's folder: %v", statErr)
	}
	if _, statErr := os.Stat(busy); statErr != nil {
		t.Fatalf("the sweep removed a folder with a recent file: %v", statErr)
	}
	if err != nil || freed.Items != 1 {
		t.Fatalf("sweep = %+v, %v; want the one old entry", freed, err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("an old entry stayed: %v", err)
	}
	if _, err := os.Stat(young); err != nil {
		t.Fatalf("a young entry was removed: %v", err)
	}
}

// The sweep removes only the folders this package makes. An unrelated program's
// old workspace, a Go tool folder and a process guard's control folder all stay,
// even when they are older than the retention.
func TestSweepKeepsUnrelatedOldTemps(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	ours := testFolder(t, root, testFolderPrefix+"4242-1", 2*testFolderRetention, false)
	keep := []string{"TestEngineHookWorker123456", "tmp.2f3a1b", "go-build987654", "dyna-pg-0123456789"}
	for _, name := range keep {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		// A live process guard holds this socket, and its folder carries no
		// process ID to check. Removing it would stop the guard being signalled.
		if err := os.WriteFile(filepath.Join(path, "control.sock"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		old := now.Add(-2 * testFolderRetention)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	alive := func(int) bool { return false }

	freed, err := sweepTestFolders(now, root, alive, false)
	if err != nil || freed.Items != 1 {
		t.Fatalf("sweep = %+v, %v; want only the test folder", freed, err)
	}
	if _, err := os.Stat(ours); !os.IsNotExist(err) {
		t.Fatalf("a stale test folder stayed: %v", err)
	}
	for _, name := range keep {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("the sweep removed %q: %v", name, err)
		}
	}
}

// testFolder makes one folder with a frozen file, as a harness bundle leaves it.
func testFolder(t *testing.T, root, name string, age time.Duration, readOnly bool) string {
	t.Helper()
	dir := filepath.Join(root, name)
	frozen := filepath.Join(dir, "dyna", "harnesses", "1.0.0", "bin")
	if err := os.MkdirAll(frozen, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(frozen, "grok-native"), []byte("payload"), 0o400); err != nil {
		t.Fatal(err)
	}
	if readOnly {
		if err := os.Chmod(filepath.Dir(frozen), 0o500); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-age)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	return dir
}

// ageTree sets the modification time of everything under dir, so a folder reads
// as old to a sweep that looks at its content.
func ageTree(t *testing.T, dir string, age time.Duration) {
	t.Helper()
	old := time.Now().Add(-age)
	err := filepath.WalkDir(dir, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(path, old, old)
	})
	if err != nil {
		t.Fatal(err)
	}
}
