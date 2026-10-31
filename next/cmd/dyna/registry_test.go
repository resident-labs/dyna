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
