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

func TestGuidanceBindings(t *testing.T) {
	r := newRootCmd()
	for _, words := range [][]string{{"account", "list"}, {"account", "quota"}, {"account", "limit"}, {"session", "set-account"}, {"addon", "set"}} {
		c, rest, err := r.Find(words)
		if err != nil || len(rest) > 0 || c == r {
			t.Fatalf("%v: %v %v", words, rest, err)
		}
	}
}

func TestDiscordPolicyAndCheckBindings(t *testing.T) {
	root := newRootCmd()
	for _, action := range []string{"policy", "check", "apply"} {
		cmd, rest, e := root.Find([]string{"discord", action})
		if e != nil || len(rest) != 0 || cmd.Name() != action || cmd.Flags().Lookup("server") == nil {
			t.Fatalf("missing Discord %s binding: %v", action, e)
		}
	}
	cmd, _, e := root.Find([]string{"room", "discord"})
	if e != nil || cmd.Flags().Lookup("policy-file") == nil {
		t.Fatal("missing policy-file binding")
	}
}
