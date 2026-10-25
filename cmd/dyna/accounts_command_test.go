package main

import "testing"

// dyna accounts lists and chooses saved accounts. Copying a personal login is a
// separate, explicit step of `dyna harness login`, never an accounts subcommand.
func TestAccountsHasNoImportCommand(t *testing.T) {
	for _, c := range newRootCommand().Commands() {
		if c.Name() != "accounts" {
			continue
		}
		for _, sub := range c.Commands() {
			if sub.Name() == "import" {
				t.Fatal("the dyna accounts command still copies personal logins")
			}
		}
	}
}
