package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestExecutableReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dyna")
	if err := os.WriteFile(path, []byte("old"), 0700); err != nil {
		t.Fatal(err)
	}
	running, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := executableChanged(path, running)
	if changed() {
		t.Fatal("unchanged executable was replaced")
	}
	replacement := path + ".new"
	if err := os.WriteFile(replacement, []byte("new"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if !changed() {
		t.Fatal("atomic replacement was missed")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if !changed() {
		t.Fatal("deleted executable was missed")
	}
}

func TestReexecInstalledRunsInstalledPathWithSameArgs(t *testing.T) {
	old := execFn
	t.Cleanup(func() { execFn = old })
	var gotPath string
	var gotArgs []string
	execFn = func(path string, args, env []string) error {
		gotPath, gotArgs = path, args
		return errors.New("stop")
	}
	want, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := ReexecInstalled(); err == nil || err.Error() != "stop" {
		t.Fatalf("ReexecInstalled = %v, want the exec error", err)
	}
	if gotPath != strings.TrimSuffix(want, " (deleted)") || !slices.Equal(gotArgs, os.Args) {
		t.Fatalf("exec %q %q, want %q %q", gotPath, gotArgs, want, os.Args)
	}
}
