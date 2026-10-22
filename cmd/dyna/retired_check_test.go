package main

import "testing"

func TestRootCommandDoesNotExposeCheck(t *testing.T) {
	for _, command := range newRootCommand().Commands() {
		if command.Name() == "check" {
			t.Fatal("retired check command remains registered")
		}
	}
}
