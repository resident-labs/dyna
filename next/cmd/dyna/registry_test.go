package main

import (
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract/tools"
)

func TestRegistryCLIBindings(t *testing.T) {
	root := newRootCmd()
	for _, entry := range tools.Entries() {
		for _, binding := range entry.CLI {
			cmd, rest, err := root.Find(strings.Fields(binding))
			if err != nil || len(rest) != 0 || cmd == root {
				t.Errorf("missing CLI binding %s", binding)
			}
		}
	}
}

func TestSharedPostHelp(t *testing.T) {
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"post"})
	if err != nil || !strings.HasPrefix(cmd.Short, "Send or update a post.") {
		t.Fatalf("post help: %q %v", cmd.Short, err)
	}
}
