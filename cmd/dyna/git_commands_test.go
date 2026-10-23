package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestGitCommandsRejectRetiredTopLevelNames(t *testing.T) {
	root := newRootCommand()
	for _, name := range []string{"pr", "change", "ci", "worktrees", "hooks"} {
		canonical, _, err := root.Find([]string{"git", name})
		if err != nil || canonical.CommandPath() != "dyna git "+name || canonical.Hidden {
			t.Fatalf("canonical command = %v, %v", canonical, err)
		}
		if _, _, err := root.Find([]string{name}); err == nil {
			t.Fatalf("retired command %s still resolves", name)
		}
	}
	alias, _, err := root.Find([]string{"git", "worktree", "list"})
	if err != nil || alias.CommandPath() != "dyna git worktrees list" {
		t.Fatalf("singular alias broke: %v, %v", alias, err)
	}
}

// TestRenamedCommandPathsResolveToTheSameCommand guards the moved and renamed
// groups: an old path and its new path must reach one command.
func TestRenamedCommandPathsResolveToTheSameCommand(t *testing.T) {
	root := newRootCommand()
	for _, tc := range []struct {
		old, new []string
		// alias marks an old group name, which reaches the same command.
		// A moved command builds its own copy under the old parent.
		alias bool
	}{
		{[]string{"subagent", "start"}, []string{"session", "start"}, true},
		{[]string{"subagent", "list"}, []string{"session", "list"}, true},
		{[]string{"git", "issue", "list"}, []string{"issue", "list"}, false},
		{[]string{"git", "export"}, []string{"git", "pr", "export"}, false},
	} {
		oldCmd, oldRest, err := root.Find(tc.old)
		if err != nil || len(oldRest) != 0 {
			t.Fatalf("old path %v: remaining=%v, err=%v", tc.old, oldRest, err)
		}
		newCmd, newRest, err := root.Find(tc.new)
		if err != nil || len(newRest) != 0 {
			t.Fatalf("new path %v: remaining=%v, err=%v", tc.new, newRest, err)
		}
		if tc.alias && oldCmd != newCmd {
			t.Errorf("%v resolves to %s, %v resolves to %s", tc.old, oldCmd.CommandPath(), tc.new, newCmd.CommandPath())
		}
		if oldCmd.Name() != newCmd.Name() || oldCmd.Short != newCmd.Short {
			t.Errorf("%v and %v reach different commands: %q %q", tc.old, tc.new, oldCmd.Name(), newCmd.Name())
		}
		if !tc.alias && (!hiddenFromHelp(oldCmd) || hiddenFromHelp(newCmd)) {
			t.Errorf("%v must stay hidden and %v must stay visible", tc.old, tc.new)
		}
	}
}

// hiddenFromHelp reports whether cmd or a parent hides cmd from help.
func hiddenFromHelp(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Hidden {
			return true
		}
	}
	return false
}

func TestGitCIReportCheck(t *testing.T) {
	run := func(args ...string) string {
		t.Helper()
		root := newRootCommand()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetIn(strings.NewReader("{\"Action\":\"pass\",\"Package\":\"example\",\"Test\":\"TestOK\",\"Elapsed\":0.01}\n"))
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out.String()
	}
	if canonical := run("git", "ci", "tests"); !strings.Contains(canonical, "1 tests") {
		t.Fatalf("report missing test count: %q", canonical)
	}
}
