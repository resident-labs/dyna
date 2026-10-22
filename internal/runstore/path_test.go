package runstore

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestValidateRunIDRejectsTraversal(t *testing.T) {
	for _, id := range []string{"", "wf_", "../wf_x", "wf_a/../wf_b", `wf_a\..\wf_b`, "not-a-run", "wf_a/b"} {
		if err := ValidateRunID(id); err == nil || !strings.Contains(err.Error(), "invalid run id") {
			t.Fatalf("ValidateRunID(%q) = %v", id, err)
		}
	}
	if err := ValidateRunID("wf_ok-1"); err != nil {
		t.Fatal(err)
	}
}

func TestConfinedRunPathRejectsSymlinkEscape(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := os.MkdirAll(RunsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "plan.json"), []byte(`{"resumedFrom":"wf_root"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(RunsDir(), "wf_escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfinedRunPath("wf_escape", "plan.json"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("escaped run dir err = %v", err)
	}

	inside := filepath.Join(RunsDir(), "wf_inside")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "plan.json"), filepath.Join(inside, "plan.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfinedRunPath("wf_inside", "plan.json"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("escaped plan.json err = %v", err)
	}

	if _, err := ConfinedRunPath("../wf_x", "plan.json"); err == nil {
		t.Fatal("traversing run id was accepted")
	}
	missing, err := ConfinedRunPath("wf_missing", "plan.json")
	if err != nil {
		t.Fatal(err)
	}
	if missing != filepath.Join(RunsDir(), "wf_missing", "plan.json") && filepath.Base(filepath.Dir(missing)) != "wf_missing" {
		t.Fatalf("missing path = %q", missing)
	}
}

func TestOpenConfinedRunFileRejectsFIFOAndCanonicalizesAlias(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := os.MkdirAll(RunsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(RunsDir(), "wf_real")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "plan.json"), []byte(`{"version":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(RunsDir(), "wf_alias")); err != nil {
		t.Fatal(err)
	}
	f, canon, err := OpenConfinedRunFile("wf_alias", "plan.json")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if canon != "wf_real" {
		t.Fatalf("canonical id = %q", canon)
	}
	if id, err := CanonicalRunID("wf_alias"); err != nil || id != "wf_real" {
		t.Fatalf("CanonicalRunID = %q %v", id, err)
	}

	fifoDir := filepath.Join(RunsDir(), "wf_fifo")
	if err := os.MkdirAll(fifoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(fifoDir, "plan.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenConfinedRunFile("wf_fifo", "plan.json"); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("FIFO plan err = %v", err)
	}

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "plan.json"), []byte(`{"version":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	swap := filepath.Join(RunsDir(), "wf_swap")
	if err := os.MkdirAll(swap, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(swap, "plan.json"), []byte(`{"version":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfinedRunPath("wf_swap", "plan.json"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(swap, "plan.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "plan.json"), filepath.Join(swap, "plan.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenConfinedRunFile("wf_swap", "plan.json"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("replaced plan.json symlink err = %v", err)
	}
}
