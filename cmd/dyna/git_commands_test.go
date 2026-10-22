package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestGitCommandsAndHiddenLegacyNames(t *testing.T) {
	for _, name := range []string{"pr", "change", "ci", "worktrees", "hooks"} {
		t.Run(name, func(t *testing.T) {
			root := newRootCommand()
			canonical, _, err := root.Find([]string{"git", name})
			if err != nil || canonical.CommandPath() != "dyna git "+name || canonical.Hidden {
				t.Fatalf("canonical command = %v, %v", canonical, err)
			}
			legacy, _, err := root.Find([]string{name})
			if err != nil || legacy.Name() != name || !legacy.Hidden {
				t.Fatalf("legacy command is missing or visible: %v, %v", legacy, err)
			}
			if canonical == legacy {
				t.Fatal("one Cobra command cannot have two parents")
			}
			for _, command := range canonical.Commands() {
				old, _, err := legacy.Find([]string{command.Name()})
				if err != nil || old.Name() != command.Name() {
					t.Fatalf("legacy route lost subcommand %s: %v", command.Name(), err)
				}
			}
		})
	}
	root := newRootCommand()
	alias, _, err := root.Find([]string{"worktree", "list"})
	if err != nil || alias.CommandPath() != "dyna worktrees list" {
		t.Fatalf("existing singular alias broke: %v, %v", alias, err)
	}
}

func TestGitCIAndLegacyCIUseTheSameReportCheck(t *testing.T) {
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
	if canonical, legacy := run("git", "ci", "tests"), run("ci", "tests"); canonical != legacy || !strings.Contains(canonical, "1 tests") {
		t.Fatalf("report behavior differs: canonical=%q legacy=%q", canonical, legacy)
	}
}
