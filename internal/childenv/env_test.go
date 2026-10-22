package childenv

import (
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestWithoutLocks(t *testing.T) {
	t.Setenv("DYNA_SUBAGENT_LOCK_FD", "invalid")
	env := []string{"KEEP=value", "DYNA_SUBAGENT_LOCK_FD=3", "DYNA_SUBAGENT_LOCK_FD=5", "DYNA_SUBAGENT_LOCK_FD_EXTRA=keep", "EMPTY="}
	original := append([]string(nil), env...)
	want := []string{"KEEP=value", "DYNA_SUBAGENT_LOCK_FD_EXTRA=keep", "EMPTY="}
	got := WithoutLocks(env)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment = %q, want %q", got, want)
	}
	got[0] = "CHANGED=value"
	if !reflect.DeepEqual(env, original) {
		t.Fatal("WithoutLocks modified its input")
	}
	if got := WithoutLocks([]string{}); got == nil || len(got) != 0 {
		t.Fatalf("empty explicit environment = %q", got)
	}
}

func TestCleanInheritedEnvironmentAndDirectory(t *testing.T) {
	t.Setenv("DYNA_SUBAGENT_LOCK_FD", "invalid")
	t.Setenv("CHILDENV_KEEP", "preserved")
	cmd := exec.Command("unused")
	cmd.Dir = t.TempDir()
	if Clean(cmd) != cmd {
		t.Fatal("Clean returned another command")
	}
	foundKeep, foundPWD := false, false
	for _, item := range cmd.Env {
		key, value, _ := strings.Cut(item, "=")
		if key == "DYNA_SUBAGENT_LOCK_FD" {
			t.Fatalf("lock marker remains: %q", item)
		}
		foundKeep = foundKeep || item == "CHILDENV_KEEP=preserved"
		foundPWD = foundPWD || (key == "PWD" && value == cmd.Dir)
	}
	if !foundKeep || !foundPWD {
		t.Fatalf("child environment lost inherited values or working directory: %q", cmd.Env)
	}
}

func TestCleanExplicitEnvironmentPreservesExtraFiles(t *testing.T) {
	t.Setenv("DYNA_SUBAGENT_LOCK_FD", "invalid")
	file, err := os.CreateTemp(t.TempDir(), "lock")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	cmd := exec.Command("unused")
	cmd.Env = []string{"ONLY=value", "DYNA_SUBAGENT_LOCK_FD=3"}
	cmd.ExtraFiles = []*os.File{file}
	Clean(cmd)
	if !reflect.DeepEqual(cmd.Env, []string{"ONLY=value"}) {
		t.Fatalf("explicit environment = %q", cmd.Env)
	}
	if len(cmd.ExtraFiles) != 1 || cmd.ExtraFiles[0] != file {
		t.Fatal("Clean changed intentional descriptor transfers")
	}
}
