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
	scope := filepath.Join(owner, ".local", "share", "dyna", "accounts", "codex", ".runtime")
	ownerEnv := []string{"HOME=" + owner, "PATH=" + filepath.Dir(git), "DYNA_NO_AUTO_UPDATE=1", "DYNA_COMPUTERS_ENABLED=0", "DYNA_SLICE=off"}
	private, err := home.Prepare(scope, bin, ownerEnv)
	if err != nil {
		t.Fatal(err)
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
