package main

import (
	"bytes"
	"strings"
	"testing"
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
