package fsutil

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

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
