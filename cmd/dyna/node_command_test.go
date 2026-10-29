package main

import "testing"

func TestNodeCommandReplacesMachine(t *testing.T) {
	found := false
	for _, command := range newRootCommand().Commands() {
		if command.HasAlias("machine") {
			t.Fatalf("%s keeps the retired machine alias", command.Name())
		}
		switch command.Name() {
		case "node":
			found = true
		case "machine":
			t.Fatal("retired machine command remains registered")
		}
	}
	if !found {
		t.Fatal("node command is not registered")
	}
}
