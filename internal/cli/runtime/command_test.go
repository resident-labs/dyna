package runtime

import (
	"bytes"
	"strings"
	"testing"

	"dyna-agent/internal/harness/runtime"
)

func TestChoosePersonal(t *testing.T) {
	found := &runtime.PersonalLogin{Harness: "codex", Label: "owner@example.test"}
	for answer, want := range map[string]bool{"\n": true, "u\n": true, "Use\n": true, "s\n": false, "sign in\n": false, "": true} {
		var out bytes.Buffer
		if got := choosePersonal(strings.NewReader(answer), &out, found); got != want {
			t.Errorf("answer %q: got %v", answer, got)
		}
		if !strings.Contains(out.String(), "owner@example.test") {
			t.Errorf("prompt does not name the account: %q", out.String())
		}
	}
	var out bytes.Buffer
	saved := *found
	saved.Saved = true
	if choosePersonal(strings.NewReader("u\n"), &out, &saved) || !strings.Contains(out.String(), "already saved") {
		t.Fatalf("saved login offered again: %q", out.String())
	}
}

func TestLoginRejectsConflictingChoices(t *testing.T) {
	c := NewCommand()
	c.SetArgs([]string{"login", "codex", "--use-personal", "--sign-in"})
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	if err := c.Execute(); err == nil || !strings.Contains(err.Error(), "use-personal") {
		t.Fatalf("got %v", err)
	}
}

func TestInstallVersionIsOptional(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	c := NewCommand()
	c.SetArgs([]string{"install", "no-such-harness"})
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	if err := c.Execute(); err == nil || !strings.Contains(err.Error(), "adapter is not available") {
		t.Fatalf("got %v", err)
	}
}

func TestRollbackWithoutTargetFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	c := NewCommand()
	c.SetArgs([]string{"rollback", "codex"})
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	if err := c.Execute(); err == nil || !strings.Contains(err.Error(), "No earlier good version") {
		t.Fatalf("got %v", err)
	}
}
