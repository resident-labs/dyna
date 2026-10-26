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

// Go test, database and tool folders fill the host temp folder too, and every
// one of them goes once it is old and its binary is gone.
func TestSweepTestFoldersRemovesOldToolTemps(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	stale := []string{
		"TestEngineHookWorker123456",
		"dyna-test-4242-1",
		"dyna-pg-4242",
		"tmp.2f3a1b",
		"go-build987654",
	}
	fresh := []string{"TestFresh123456", "go-buildfresh"}
	running := testFolder(t, root, "dyna-test-4343-2", 2*testFolderRetention, false)
	for _, name := range stale {
		testFolder(t, root, name, 2*testFolderRetention, false)
	}
	for _, name := range fresh {
		testFolder(t, root, name, 0, false)
	}
	other := testFolder(t, root, "build-cache", 2*testFolderRetention, false)
	alive := func(pid int) bool { return pid == 4343 }

	freed, err := sweepTestFolders(now, root, alive, false)
	if err != nil || freed.Items != len(stale) {
		t.Fatalf("sweep = %+v, %v; want %d folders", freed, err, len(stale))
	}
	for _, name := range stale {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("stale tool folder %q stayed: %v", name, err)
		}
	}
	for _, name := range append(fresh, filepath.Base(running), filepath.Base(other)) {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("folder %q must stay: %v", name, err)
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
