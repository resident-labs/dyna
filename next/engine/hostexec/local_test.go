package hostexec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestLocalExportsOwnerCLIEnvironment(t *testing.T) {
	dir := t.TempDir()
	// Execute a stand-in for bash so the test sees the actual child environment, without systemd.
	if err := os.WriteFile(filepath.Join(dir, "bash"), []byte("#!/bin/sh\nprintf '%s\\n%s\\n' \"$DYNA_SOCKET\" \"$PATH\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	binary, _ := os.Executable()
	for _, a := range []contract.HostexecRunArgs{{Argv: []string{"dyna", "room", "list"}}, {Collect: true}, {Attach: true}} {
		out, code, _, err := (Local{Socket: "/live/runtime/owner.sock"}).RunHostexec(t.Context(), a, "test")
		want := "/live/runtime/owner.sock\n" + filepath.Dir(binary) + ":" + dir + "\n"
		if string(out) != want || err != nil || code != 0 {
			t.Fatalf("got %q code=%d err=%v, want %q", out, code, err, want)
		}
		if !strings.HasPrefix(strings.Split(string(out), "\n")[1], filepath.Dir(binary)+":") {
			t.Fatal("binary directory not first")
		}
	}
}
