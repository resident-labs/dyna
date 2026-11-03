package hostexec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// bashOf runs the wrapper of RunArgs without systemd-run, which a test machine may lack: a fake one runs what follows "--".
func bashOf(t *testing.T, a contract.HostexecRunArgs, op contract.OpID, env ...string) string {
	t.Helper()
	bin := t.TempDir()
	fake := "#!/bin/bash\nwhile [ \"$1\" != -- ]; do shift; done; shift; exec \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "systemd-run"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte("#!/bin/bash\necho DISPLAY=:7\necho WAYLAND_DISPLAY=wayland-1\necho SECRET=no\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	args := RunArgs(a, op)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append([]string{"PATH=" + bin + ":/usr/bin:/bin", "TMPDIR=" + t.TempDir()}, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return string(out)
}

func TestRunGetsAnEmptyDynaOut(t *testing.T) {
	tmp := t.TempDir()
	a := contract.HostexecRunArgs{Argv: []string{"bash", "-c", `ls -A "$DYNA_OUT" | wc -l; stat -c %a "$DYNA_OUT"; echo hi > "$DYNA_OUT/a.txt"`}, Timeout: 1000}
	out := bashOf(t, a, "op1", "TMPDIR="+tmp)
	if out != "0\n700\n" {
		t.Fatalf("out %q", out)
	}
	if b, err := os.ReadFile(filepath.Join(tmp, "dyna-out-op1", "a.txt")); err != nil || string(b) != "hi\n" {
		t.Fatalf("file %q %v", b, err)
	}
}

func TestDesktopEnvironmentOnlyWhenAsked(t *testing.T) {
	a := contract.HostexecRunArgs{Argv: []string{"bash", "-c", `echo "$DISPLAY|$WAYLAND_DISPLAY|$SECRET"`}, Timeout: 1000}
	if out := bashOf(t, a, "op2", "DISPLAY=:0"); out != ":0||\n" {
		t.Fatalf("plain run: %q", out)
	}
	a.Desktop = true
	if out := bashOf(t, a, "op3", "DISPLAY=:0"); out != ":7|wayland-1|\n" {
		t.Fatalf("desktop run: %q", out)
	}
}

// The collect run prints the files as a tar and removes the folder; with no files it prints nothing.
func TestCollectReadsBackAndRemoves(t *testing.T) {
	tmp := t.TempDir()
	run := func(op contract.OpID) []byte {
		args := RunArgs(CollectArgs(contract.HostexecRunArgs{}, op), op+"-out")
		cmd := exec.Command(args[0], args[1:]...)
		bin := t.TempDir()
		os.WriteFile(filepath.Join(bin, "systemd-run"), []byte("#!/bin/bash\nwhile [ \"$1\" != -- ]; do shift; done; shift; exec \"$@\"\n"), 0o755)
		cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "TMPDIR=" + tmp}
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	dir := filepath.Join(tmp, "dyna-out-a")
	os.MkdirAll(filepath.Join(dir, "sub"), 0o700)
	os.WriteFile(filepath.Join(dir, "sub", "s.png"), []byte("png"), 0o600)
	if out := run("a"); !strings.Contains(string(out), "sub/s.png") {
		t.Fatalf("tar %q", out)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("folder stays: %v", err)
	}
	os.MkdirAll(filepath.Join(tmp, "dyna-out-b"), 0o700)
	if out := run("b"); len(out) != 0 {
		t.Fatalf("empty folder gave %q", out)
	}
}

// A collect run's stderr stays apart from its stdout, so a tar warning cannot corrupt the archive.
func TestCollectRunKeepsStderrApart(t *testing.T) {
	a := CollectArgs(contract.HostexecRunArgs{}, "op1")
	a.Argv = []string{"bash", "-c", "echo data; echo warn >&2"}
	if out := strings.TrimSpace(bashOfStdout(t, a)); out != "data" {
		t.Fatalf("out %q", out)
	}
}

func bashOfStdout(t *testing.T, a contract.HostexecRunArgs) string {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "systemd-run"), []byte("#!/bin/bash\nwhile [ \"$1\" != -- ]; do shift; done; shift; exec \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	args := RunArgs(a, "op1")
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "TMPDIR=" + t.TempDir()}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
