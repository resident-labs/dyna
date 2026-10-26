package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/harness/home"
	"dyna-agent/internal/room"
	"dyna-agent/internal/shellquote"
)

// A dyna started by absolute path inside a private harness HOME, as the
// computer Chromium wrapper does, must use the owner's data tree.
func TestDynaByPathInPrivateHomeUsesOwnerData(t *testing.T) {
	bin := dynaBinary(t)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	owner := t.TempDir()
	scope := filepath.Join(owner, ".local", "share", "dyna", "accounts", "pi", ".runtime")
	ownerEnv := []string{"HOME=" + owner, "DYNA_NO_AUTO_UPDATE=1", "DYNA_COMPUTERS_ENABLED=0", "DYNA_SLICE=off"}
	// Preparing a private home asks the owner's `go` for the Go roots, and waits
	// out a two second deadline when the answer does not come. Name the roots
	// here, so nothing is asked, and put a stub Go first on PATH that records
	// being run: the assertion below then fails if the probe ever runs again.
	ownerEnv = append(ownerEnv,
		"GOPATH="+filepath.Join(owner, "go"),
		"GOMODCACHE="+filepath.Join(owner, "go", "pkg", "mod"),
		"GOCACHE="+filepath.Join(owner, "cache", "go-build"))
	probed := filepath.Join(t.TempDir(), "go-was-probed")
	stub := t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, "go"), []byte("#!/bin/sh\n: > "+shellquote.Quote(probed)+"\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ownerEnv = append(ownerEnv, "PATH="+stub+string(os.PathListSeparator)+filepath.Dir(git))
	private, err := home.Prepare(scope, bin, ownerEnv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(probed); !os.IsNotExist(err) {
		t.Fatalf("preparing the private home asked the owner's Go for its roots: %v", err)
	}
	env := append([]string(nil), ownerEnv...)
	for key, value := range private {
		env = append(env, key+"="+value)
	}
	// Writes go through the engine, so the probe reads a room that only the
	// owner's data tree holds.
	ownerData := filepath.Join(owner, ".local", "share")
	t.Setenv("XDG_DATA_HOME", ownerData)
	t.Cleanup(func() { _ = database.Close(filepath.Join(ownerData, "dyna")) })
	if _, err := room.DefaultStore().Create(room.CreateInput{Name: "probe", CreatedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "room", "list")
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "probe") {
		t.Fatalf("dyna room list did not read the owner's data tree: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(home.Home(scope), ".local", "share", "dyna")); !os.IsNotExist(err) {
		t.Fatalf("dyna created a data tree inside the private HOME: %v", err)
	}
}
