package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestGitSyncCommandWiring(t *testing.T) {
	for _, name := range []string{"status", "fetch"} {
		root := newRootCommand()
		cmd, args, err := root.Find([]string{"git", "sync", name})
		if err != nil || cmd.CommandPath() != "dyna git sync "+name || len(args) != 0 {
			t.Fatalf("%s: command=%s args=%v error=%v", name, cmd.CommandPath(), args, err)
		}
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := newRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"git", "sync", "status", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "{}" {
		t.Fatalf("empty JSON status: %s", out.String())
	}
}
