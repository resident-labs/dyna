package testsafe

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A test binary that starts with real data, config, cache and runtime
// folders, as an agent's go test on the owner's laptop does, runs with
// temporary ones, and creates nothing in the real ones.
func TestTestBinaryStartsWithTemporaryFolders(t *testing.T) {
	if os.Getenv("TESTSAFE_CHILD") == "1" {
		for _, key := range []string{"XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"} {
			if err := Check(os.Getenv(key)); err != nil {
				t.Fatalf("%s: %v", key, err)
			}
		}
		return
	}
	real := filepath.Join(filepath.Dir(tempRoot), "dyna-testsafe-real")
	cmd := exec.Command(os.Args[0], "-test.run=^TestTestBinaryStartsWithTemporaryFolders$")
	// The config and cache homes come from HOME, as when XDG leaves them unset.
	cmd.Env = append(os.Environ(), "TESTSAFE_CHILD=1", "HOME="+real, "XDG_CONFIG_HOME=", "XDG_CACHE_HOME=",
		"XDG_DATA_HOME="+real+"/share", "XDG_RUNTIME_DIR="+real+"/run")
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "testsafe:") {
		t.Fatalf("child test binary: %v\n%s", err, out)
	}
	if _, err := os.Stat(real); !os.IsNotExist(err) {
		t.Fatalf("the real folders were touched: %v", err)
	}
	// A folder the test sets itself is kept.
	own := t.TempDir()
	if err := Check(own); err != nil {
		t.Fatal(err)
	}
}

// A test binary that a test starts with a TMPDIR of its own keeps the
// folders its parent chose, so the two still meet.
func TestChildTestBinaryKeepsItsParentsFolders(t *testing.T) {
	if os.Getenv("TESTSAFE_CHILD") == "2" {
		if got := os.Getenv("XDG_DATA_HOME"); got != os.Getenv("TESTSAFE_WANT") {
			t.Fatalf("XDG_DATA_HOME = %s in the child; want the parent's %s", got, os.Getenv("TESTSAFE_WANT"))
		}
		return
	}
	data := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestChildTestBinaryKeepsItsParentsFolders$")
	cmd.Env = append(os.Environ(), "TESTSAFE_CHILD=2", "TMPDIR="+t.TempDir(), "XDG_DATA_HOME="+data, "TESTSAFE_WANT="+data)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child test binary: %v\n%s", err, out)
	}
}

// A DYNA_TESTSAFE_ROOT that no test binary marked, such as a hand-set "/",
// is ignored: the child keeps its own temporary root.
func TestUnmarkedRootIsIgnored(t *testing.T) {
	if os.Getenv("TESTSAFE_CHILD") == "3" {
		if tempRoot == "/" {
			t.Fatal("an unmarked root was accepted")
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestUnmarkedRootIsIgnored$")
	cmd.Env = append(os.Environ(), "TESTSAFE_CHILD=3", rootEnv+"=/")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child test binary: %v\n%s", err, out)
	}
}
