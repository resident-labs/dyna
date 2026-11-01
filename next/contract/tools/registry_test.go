package tools

import (
	"reflect"
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
		if b.Tool == "" || b.Name == "" || b.Help == "" || len(b.CLI) == 0 || b.Request == nil || b.Request.Kind() != reflect.Struct || b.Handler == NoBoxHandler || b.Handler > ComputerScreenshot {
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
		"context": "show", "question": "ask list", "post": "send", "session": "start list stop close restart set_profile move", "room": "log repos pause", "op": "withdraw", "hostexec": "run request_grant", "pr": "open list status track untrack", "issue": "list status track untrack", "visual": "check", "computer": "open click type text screenshot", "goal": "set list item done journal",
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
