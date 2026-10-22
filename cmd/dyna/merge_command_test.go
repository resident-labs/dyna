package main

import "testing"

func TestLandingHasNoAgentCommand(t *testing.T) {
	root := newRootCommand()
	cmd, _, err := root.Find([]string{"land"})
	if err == nil && cmd != root {
		t.Fatal("dyna land still exposes an agent landing action")
	}
	cmd, _, err = root.Find([]string{"git", "pr", "merge-status"})
	if err != nil || cmd.Name() != "merge-status" {
		t.Fatalf("merge-status is missing: %v", err)
	}
}
