package main

import "testing"

func TestStateSnapshotCommandIsWired(t *testing.T) {
	cmd, _, err := newRootCommand().Find([]string{"state", "snapshot"})
	if err != nil {
		t.Fatal(err)
	}
	if got := cmd.CommandPath(); got != "dyna state snapshot" {
		t.Fatalf("command path = %q, want dyna state snapshot", got)
	}
}
