package fsutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRunningExecutableSurvivesReplacement(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux executable inode")
	}
	if path := os.Getenv("DYNA_EXECUTABLE_REPLACEMENT_TEST"); path != "" {
		replacement := path + ".new"
		if err := os.WriteFile(replacement, []byte("new build"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, path); err != nil {
			t.Fatal(err)
		}
		pinned, err := RunningExecutable()
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.Stat(pinned)
		if err != nil {
			t.Fatal(err)
		}
		running, err := os.Stat("/proc/self/exe")
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(got, running) {
			t.Fatal("worker executable changed with installed build")
		}
		return
	}
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "dyna")
	data, err := os.ReadFile(current)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, "-test.run=^TestRunningExecutableSurvivesReplacement$")
	cmd.Env = append(os.Environ(), "DYNA_EXECUTABLE_REPLACEMENT_TEST="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, output)
	}
}
