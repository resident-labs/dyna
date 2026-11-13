package hostexec

import (
	"github.com/resident-labs/dyna/contract"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalExportsOwnerCLIEnvironment(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bash"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	out, code, _, err := (Local{Socket: "/live/runtime/owner.sock"}).RunHostexec(t.Context(), contract.HostexecRunArgs{Argv: []string{"dyna", "room", "list"}}, "test")
	binary, _ := os.Executable()
	for _, want := range []string{"/live/runtime/owner.sock", filepath.Dir(binary), `export PATH="$2:$PATH"`, "dyna\nroom\nlist"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("missing %q: %s", want, out)
		}
	}
	if err != nil || code != 0 {
		t.Fatalf("%d %v", code, err)
	}
}
