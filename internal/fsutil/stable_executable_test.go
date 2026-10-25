package fsutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Every published wrapper execs the copy the current link names. Pruning older
// images must keep it, or the next harness turn starts a wrapper with no
// executable.
func TestPruneKeepsCurrentImage(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "dyna-0000000000000001")
	if err := os.WriteFile(current, []byte("image"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(current, filepath.Join(dir, currentImageLink)); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-time.Hour)
	if err := os.Chtimes(current, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < stableImageCopies; i++ {
		newer := filepath.Join(dir, "dyna-100000000000000"+string(rune('0'+i)))
		if err := os.WriteFile(newer, []byte("image"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(newer, stamp.Add(time.Duration(i)*time.Minute), stamp.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	pruneStableExecutables(dir, filepath.Join(dir, "dyna-2000000000000000"))
	if _, err := os.Stat(current); err != nil {
		t.Fatalf("pruned the image the current link names: %v", err)
	}
}

// dyna publishes this path as $DYNA_BIN, in every harness MCP server command
// and in the tool home it hands to workers. A turn that ends must not take the
// binary with it, or the next turn starts an MCP server that cannot run and
// the worker loses every Dyna tool.
func TestStableExecutableOutlivesItsProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux running image copy")
	}
	if out := os.Getenv("DYNA_STABLE_EXECUTABLE_OUT"); out != "" {
		path, err := StableExecutable()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, []byte(path), 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := t.TempDir()
	names := make([]string, 0, 2)
	for run := 0; run < 2; run++ {
		out := filepath.Join(dir, "path-"+string(rune('a'+run)))
		cmd := exec.Command(os.Args[0], "-test.run=TestStableExecutableOutlivesItsProcess$")
		cmd.Env = append(os.Environ(), "DYNA_STABLE_EXECUTABLE_OUT="+out)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("child failed: %v\n%s", err, output)
		}
		raw, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, string(raw))
	}
	// The two children ran as different processes. A pinned /proc/<pid>/exe
	// path differs between them and names a process that has already exited.
	if names[0] != names[1] {
		t.Fatalf("stable executable changed between processes: %q then %q", names[0], names[1])
	}
	info, err := os.Stat(names[0])
	if err != nil {
		t.Fatalf("stable executable is gone after its processes exited: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0700 != 0700 || info.Size() == 0 {
		t.Fatalf("stable executable is not a runnable copy: %v", info.Mode())
	}
}

// A long-lived session MCP server or an idle harness child keeps running the
// image its turn published. Pruning must not delete that image while it runs,
// or the next launch of the same path fails.
func TestPruneKeepsRunningImage(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "dyna-old")
	live := filepath.Join(dir, "dyna-live")
	for _, path := range []string{old, live} {
		if err := os.WriteFile(path, []byte("image"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	stamp := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < stableImageCopies; i++ {
		path := filepath.Join(dir, "dyna-new-"+string(rune('a'+i)))
		if err := os.WriteFile(path, []byte("image"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, stamp.Add(time.Duration(i)*time.Minute), stamp.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	pruneImages(dir, filepath.Join(dir, "dyna-keep"), map[string]bool{live: true})
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("pruned the image a live process runs: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("kept an idle superseded image: %v", err)
	}
}
