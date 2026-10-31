package main

import (
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract/tools"
)

func TestRegistryCLIBindings(t *testing.T) {
	root := newRootCmd()
	check := func(cli []string) {
		for _, binding := range cli {
			cmd, rest, err := root.Find(strings.Fields(binding))
			if err != nil || len(rest) != 0 || cmd == root {
				t.Errorf("missing CLI binding %s", binding)
			}
		}
	}
	for _, a := range tools.Actions() {
		check(a.CLI)
	}
	for _, b := range tools.BoxActions() {
		check(b.CLI)
	}
}

func TestSharedPostHelp(t *testing.T) {
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"post"})
	if err != nil || !strings.HasPrefix(cmd.Short, "Send or update a post.") {
		t.Fatalf("post help: %q %v", cmd.Short, err)
	}
}
