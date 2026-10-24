package main

import (
	"bytes"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
)

// serviceAuthorityActions names actions with no exact CLI command, or whose
// CLI also supports unrestricted reads or other operations.
var serviceAuthorityActions = map[string]bool{
	"SetRoomChangePolicy":     true,
	"SetRoomCIBudget":         true,
	"SendDiffComments":        true,
	"SendWorktreeComments":    true,
	"SetGitHubAccess":         true,
	"SetPRSettings":           true,
	"SetServiceProfile":       true,
	"ConfigCheckProfile":      true,
	"SyncPrivateRuntime":      true,
	"ResolveFinding":          true,
	"DismissSession":          true,
	"ChangeIssue":             true,
	"LinkIssueGoal":           true,
	"SetChangePolicyOverride": true,
	"SetPRGoal":               true,
	"UpdateChangeNotes":       true,
	"post @voice-call":        true,
	// goal amend is a flag of `dyna goal edit`, not a command path of its own.
	"goal amend": true,
}

func TestAuthorityRulesResolveToCommands(t *testing.T) {
	root := newRootCommand()
	for action := range serviceAuthorityActions {
		found := false
		for _, rule := range authority.Rules {
			found = found || rule.Action == action
		}
		if !found {
			t.Errorf("service action %q has no rule", action)
		}
	}
	for _, rule := range authority.Rules {
		if serviceAuthorityActions[rule.Action] {
			continue
		}
		cmd, remaining, err := root.Find(strings.Fields(rule.Action))
		if err != nil || len(remaining) != 0 || authorityText(cmd) != authority.Who(rule.Action) {
			t.Errorf("authority action %q has no exact command: remaining=%v, err=%v", rule.Action, remaining, err)
		}
	}
}

func TestAuthorityHelpShowsWho(t *testing.T) {
	root := newRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	for _, rule := range authority.Rules {
		if serviceAuthorityActions[rule.Action] {
			continue
		}
		cmd, remaining, err := root.Find(strings.Fields(rule.Action))
		if err != nil || len(remaining) != 0 {
			t.Fatalf("find %q: remaining=%v, err=%v", rule.Action, remaining, err)
		}
		who := authority.Who(rule.Action)
		if who == "" {
			t.Fatalf("authority action %q has no Who text", rule.Action)
		}
		out.Reset()
		if err := cmd.Help(); err != nil {
			t.Fatal(err)
		}
		if strings.Count(out.String(), who) != 1 {
			t.Errorf("%q help must show Who once: %s", rule.Action, out.String())
		}
	}
}
