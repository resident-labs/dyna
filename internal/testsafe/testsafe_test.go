package testsafe

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGitConfigWithoutAutoMaintenance(t *testing.T) {
	for _, inherited := range []int{1, 2} {
		t.Run(strconv.Itoa(inherited)+" inherited entries", func(t *testing.T) {
			env := []string{
				"HOME=" + t.TempDir(),
				"PATH=" + os.Getenv("PATH"),
				"GIT_CONFIG_COUNT=" + strconv.Itoa(inherited),
			}
			want := map[string]string{"gc.auto": "0\n", "maintenance.auto": "false\n", "core.fsync": "none\n"}
			for i := 0; i < inherited; i++ {
				key := "test.inherited" + strconv.Itoa(i)
				value := "value" + strconv.Itoa(i)
				env = append(env,
					"GIT_CONFIG_KEY_"+strconv.Itoa(i)+"="+key,
					"GIT_CONFIG_VALUE_"+strconv.Itoa(i)+"="+value,
				)
				want[key] = value + "\n"
			}
			env = GitConfigWithoutAutoMaintenance(env)
			env = appendGitConfig(env, "core.fsync", "none")
			for key, expected := range want {
				cmd := exec.Command("git", "config", "--get", key)
				cmd.Env = env
				cmd.Dir = t.TempDir()
				out, err := cmd.CombinedOutput()
				if err != nil || string(out) != expected {
					t.Fatalf("git config %s = %q, %v; want %q", key, out, err, expected)
				}
			}
		})
	}
}

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
		if dir := os.Getenv("DYNA_DATA_DIR"); dir != "" {
			t.Fatalf("DYNA_DATA_DIR = %s survived outside the temporary folder", dir)
		}
		return
	}
	real := filepath.Join(filepath.Dir(tempRoot), "dyna-testsafe-real")
	cmd := exec.Command(os.Args[0], "-test.run=^TestTestBinaryStartsWithTemporaryFolders$")
	// The config and cache homes come from HOME, as when XDG leaves them unset.
	cmd.Env = append(os.Environ(), "TESTSAFE_CHILD=1", "HOME="+real, "XDG_CONFIG_HOME=", "XDG_CACHE_HOME=",
		"XDG_DATA_HOME="+real+"/share", "XDG_RUNTIME_DIR="+real+"/run", "DYNA_DATA_DIR="+real+"/dyna")
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

// A later test binary removes the folders of test binaries that have ended,
// and keeps the folder of one that still runs, a folder that is too young to
// judge, and a folder of an older Dyna build.
func TestPruneStaleRemovesEndedBinaryFolders(t *testing.T) {
	root := t.TempDir()
	made := func(name string, age time.Duration, mode fs.FileMode) string {
		dir := filepath.Join(root, name)
		frozen := filepath.Join(dir, "dyna", "harnesses", "1.0.0", "bin")
		if err := os.MkdirAll(frozen, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(frozen, "grok-native"), []byte("payload"), 0o400); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Dir(frozen), mode); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-age)
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	ended := made(tempPrefix+"424242-1234", 2*staleAfter, 0o500)
	running := made(tempPrefix+"434343-1234", 2*staleAfter, 0o700)
	young := made(tempPrefix+"444444-1234", 0, 0o700)
	legacy := made(tempPrefix+"123456789", 2*staleAfter, 0o700)
	pruneStale(root, time.Now(), func(pid int) bool { return pid == 434343 })
	for _, dir := range []string{ended} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("a folder of an ended test binary stayed: %v", err)
		}
	}
	for _, dir := range []string{running, young, legacy} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("a folder that must stay was removed: %v", err)
		}
	}
}

func TestFolderPID(t *testing.T) {
	for _, tt := range []struct {
		name string
		pid  int
		ok   bool
	}{
		{tempPrefix + "4242-1234", 4242, true},
		{tempPrefix + "123456789", 0, false},
		{tempPrefix + "nan-1234", 0, false},
		{"other-4242-1234", 0, false},
	} {
		pid, ok := folderPID(tt.name)
		if pid != tt.pid || ok != tt.ok {
			t.Fatalf("folderPID(%q) = %d, %v", tt.name, pid, ok)
		}
	}
}

// A session exports a long disk TMPDIR and GOTMPDIR. A test binary ignores them:
// t.TempDir would otherwise sit under them and break the unix sockets and the
// folder check of tests that run inside a session.
func TestTestBinaryIgnoresSessionTemporaryFolder(t *testing.T) {
	const session = "/home/owner/.cache/dyna/tmp/0a1b2c3d"
	if os.Getenv("TESTSAFE_TMP_CHILD") == "1" {
		if dir := t.TempDir(); strings.Contains(dir, "/.cache/dyna/tmp") {
			t.Fatalf("t.TempDir = %s sits under the session's temporary folder", dir)
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestTestBinaryIgnoresSessionTemporaryFolder$")
	cmd.Env = append(os.Environ(), "TESTSAFE_TMP_CHILD=1", "TMPDIR="+session, "TMP="+session, "TEMP="+session, "GOTMPDIR="+session)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child test binary: %v\n%s", err, out)
	}
}
