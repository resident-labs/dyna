package main

import "testing"

// Personal logins are copied only through `dyna harness login --use-personal`.
func TestRootHasNoAccountsImportCommand(t *testing.T) {
	for _, c := range newRootCommand().Commands() {
		if c.Name() == "accounts" {
			t.Fatal("the dyna accounts command still copies personal logins")
		}
	}
}
