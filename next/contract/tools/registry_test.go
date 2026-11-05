package tools

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// The real table is well formed: unique methods, unique actions, unique CLI
// bindings, known operation kinds, and box handlers in range.
func TestRegistryTableIsValid(t *testing.T) {
	seenMethod := map[contract.Method]bool{}
	seenAction := map[string]bool{}
	seenCLI := map[string]bool{}
	for _, row := range Methods() {
		if row.Method == "" || row.Access < OwnerAccess || row.Access > OwnerLocalAccess {
			t.Errorf("invalid method row %q", row.Method)
		}
		if seenMethod[row.Method] {
			t.Errorf("duplicate method row %q", row.Method)
		}
		seenMethod[row.Method] = true
		for _, a := range row.Actions {
			meta, ok := ToolkitFor(a.Tool)
			if !ok || meta.Name != a.Tool || meta.Title == "" {
				t.Errorf("missing toolkit metadata for %s", a.Tool)
			}
			key := a.Tool + "/" + a.Name
			if a.Tool == "" || a.Name == "" || a.Help == "" || len(a.CLI) == 0 || a.Request == nil || a.Request.Kind() != reflect.Struct {
				t.Errorf("invalid action %s", key)
				continue
			}
			if a.Op != "" {
				known := false
				for _, kind := range contract.AllOpKinds {
					if a.Op == kind {
						known = true
						break
					}
				}
				if !known || row.Method != contract.MethodOpSubmit {
					t.Errorf("unknown operation route %s", key)
				}
			}
			for _, binding := range a.CLI {
				if binding == "" {
					t.Errorf("empty CLI binding %s", key)
				}
				if seenCLI[binding] {
					t.Errorf("duplicate CLI binding %s", binding)
				}
				seenCLI[binding] = true
			}
			if seenAction[key] {
				t.Errorf("duplicate action %s", key)
			}
			seenAction[key] = true
		}
	}
	for _, b := range BoxActions() {
		meta, ok := ToolkitFor(b.Tool)
		if !ok || meta.Name != b.Tool || meta.Title == "" {
			t.Errorf("missing toolkit metadata for %s", b.Tool)
		}
		key := b.Tool + "/" + b.Name
		if b.Tool == "" || b.Name == "" || b.Help == "" || len(b.CLI) == 0 || b.Request == nil || b.Request.Kind() != reflect.Struct || b.Handler == NoBoxHandler || b.Handler > lastBoxHandler {
			t.Errorf("invalid box action %s", key)
			continue
		}
		for _, binding := range b.CLI {
			if seenCLI[binding] {
				t.Errorf("duplicate CLI binding %s", binding)
			}
			seenCLI[binding] = true
		}
		if seenAction[key] {
			t.Errorf("duplicate action %s", key)
		}
		seenAction[key] = true
	}
}

// The table has exactly one row per Method constant, and no row without one;
// contractgen emits the constant list the table is checked against.
func TestMethodTableCoversEveryConstant(t *testing.T) {
	rows := Methods()
	count := map[contract.Method]int{}
	for _, row := range rows {
		count[row.Method]++
	}
	for _, m := range contract.AllMethods {
		if count[m] != 1 {
			t.Errorf("method %s has %d rows, want 1", m, count[m])
		}
		delete(count, m)
	}
	for extra := range count {
		t.Errorf("row %s is not a Method constant", extra)
	}
}

func TestAgentActionsHideOwnerMethods(t *testing.T) {
	hidden := map[string]bool{}
	for _, row := range Methods() {
		if row.Access == OwnerAccess || row.Access == OwnerLocalAccess {
			for _, a := range row.Actions {
				hidden[a.Tool+"/"+a.Name] = true
			}
		}
	}
	for _, a := range AgentActions() {
		if hidden[a.Tool+"/"+a.Name] {
			t.Fatalf("owner action %s/%s exposed", a.Tool, a.Name)
		}
	}
}

func TestPhaseOneActionCoverage(t *testing.T) {
	required := map[string]string{
		"context": "show", "question": "ask list", "post": "send", "session": "start list stop close restart set_profile move", "room": "log repos pause", "op": "withdraw", "hostexec": "request_grant", "pr": "open list status track untrack", "issue": "list status track untrack", "visual": "check", "computer": "open click move drag scroll key type text wait screenshot launch stop", "goal": "set list item done journal",
	}
	found := map[string]bool{}
	for _, a := range AgentActions() {
		found[a.Tool+"/"+a.Name] = true
	}
	for _, b := range BoxActions() {
		found[b.Tool+"/"+b.Name] = true
	}
	for tool, names := range required {
		for _, name := range strings.Fields(names) {
			if !found[tool+"/"+name] {
				t.Errorf("missing %s/%s", tool, name)
			}
		}
	}
}

// pool.set is the owner's, for Settings > Pools: agents' pool tool keeps add and list, and nothing else of pool.
func TestPoolSetIsOwnerOnlyAndAgentsPoolToolIsUnchanged(t *testing.T) {
	for _, row := range Methods() {
		if row.Method == contract.MethodPoolSet && (row.Access != OwnerAccess || len(row.Actions) != 0) {
			t.Fatalf("pool.set row: %+v", row)
		}
	}
	var names []string
	for _, a := range AgentActions() {
		if a.Tool == "pool" {
			names = append(names, a.Name)
		}
	}
	if strings.Join(names, " ") != "add list" {
		t.Fatalf("agents' pool actions: %v", names)
	}
}

// The pr toolkit's description is the one place that says how CI, tracking and merging work, so track's help does not repeat the wake.
func TestPrToolkitDescriptionIsTheHomeOfCIAndTracking(t *testing.T) {
	meta, ok := ToolkitFor("pr")
	if !ok {
		t.Fatal("no pr toolkit")
	}
	want := "CI is GitHub Actions, from each repository's .github/workflows: tests in a box show a change works there, CI shows it on GitHub. A PR you open is tracked for you, and track adds others. The engine polls tracked PRs and wakes the session that owns one on CI results, reviews, comments, conflicts, merges and closes; dyna_holds lists yours. Opening a PR merges nothing. Boxes have no gh: comments and merges run on a host through host exec, by root or a session granted it, as the room's rules say."
	if meta.Description != want {
		t.Fatalf("pr description:\n%s\nwant:\n%s", meta.Description, want)
	}
	for _, a := range AgentActions() {
		if a.Tool == "pr" && a.Name == "track" && strings.Contains(a.Help, "woken") {
			t.Fatalf("pr track help repeats the wake: %s", a.Help)
		}
	}
}

// The session tool's help is the one home of the fact that an ancestor acts on sessions under it at any depth.
func TestSessionActionsSayAnyDepth(t *testing.T) {
	seen := 0
	for _, a := range AgentActions() {
		if a.Tool != "session" {
			continue
		}
		switch a.Name {
		case "close", "move", "stop", "restart", "set_profile", "refresh":
			seen++
			if !strings.Contains(a.Help, "any session under you at any depth") {
				t.Errorf("session %s help does not say any depth: %s", a.Name, a.Help)
			}
		}
	}
	if seen != 6 {
		t.Fatalf("checked %d session actions, want 6", seen)
	}
}

// The workflow settings are the owner's alone, for the CLI and Settings > Workflows; agents' workflow tool has no action for them.
func TestWorkflowSettingsMethodsAreOwnerOnlyWithNoAgentAction(t *testing.T) {
	seen := map[contract.Method]bool{}
	for _, row := range Methods() {
		if row.Method == contract.MethodWorkflowSet || row.Method == contract.MethodWorkflowSettings {
			seen[row.Method] = true
			if row.Access != OwnerAccess || len(row.Actions) != 0 {
				t.Errorf("%s row: %+v", row.Method, row)
			}
		}
	}
	if len(seen) != 2 {
		t.Fatalf("rows found for %v", seen)
	}
}

// room.set, which carries rootApproves and githubWrites, is the owner's alone: the room tool agents have keeps log, hosts, pause and
// repos and never gains set.
func TestRoomSetIsOwnerOnlyAndAgentsRoomToolIsUnchanged(t *testing.T) {
	found := false
	for _, row := range Methods() {
		if row.Method == contract.MethodRoomSet {
			found = true
			if row.Access != OwnerAccess {
				t.Errorf("room.set row: %+v", row)
			}
		}
	}
	if !found {
		t.Fatal("no room.set row")
	}
	var names []string
	for _, a := range AgentActions() {
		if a.Tool == "room" {
			names = append(names, a.Name)
		}
	}
	slices.Sort(names)
	if strings.Join(names, " ") != "hosts log pause repos" {
		t.Fatalf("agents' room actions: %v", names)
	}
}

// service.logs is not masked until the agent tool lands, so it needs the admin scope.
func TestServiceLogsNeedAdminScope(t *testing.T) {
	for _, row := range Methods() {
		if row.Method == contract.MethodServiceLogs && row.Scope != contract.ScopeAdmin {
			t.Fatalf("service.logs scope %s", row.Scope)
		}
	}
}
