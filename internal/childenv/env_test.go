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

func TestMergeDropsDynaKeysAndSetsTerm(t *testing.T) {
	cases := []struct {
		name      string
		base      []string
		overrides []string
		term      string
		want      []string
	}{
		{
			name:      "overrides replace base and DYNA_ keys stay out",
			base:      []string{"PATH=/bin", "DYNA_SESSION=ses_1", "HOME=/home/a", "TERM=xterm"},
			overrides: []string{"HOME=/home/b", "DYNA_ROOM=room_1", "LANG=en_GB.UTF-8"},
			term:      "dumb",
			want:      []string{"HOME=/home/b", "LANG=en_GB.UTF-8", "PATH=/bin", "TERM=dumb"},
		},
		{
			name:      "the caller term replaces a base TERM",
			base:      []string{"TERM=xterm", "PATH=/bin"},
			overrides: nil,
			term:      "xterm-256color",
			want:      []string{"PATH=/bin", "TERM=xterm-256color"},
		},
		{
			name:      "entries without a key and value pair are dropped",
			base:      []string{"BROKEN", "KEEP=1"},
			overrides: []string{"ALSO_BROKEN"},
			term:      "dumb",
			want:      []string{"KEEP=1", "TERM=dumb"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Merge(c.base, c.overrides, c.term); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("Merge = %q, want %q", got, c.want)
			}
		})
	}
}
