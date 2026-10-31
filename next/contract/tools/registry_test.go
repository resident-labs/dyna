package tools

import (
	"reflect"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// The table has exactly one row per Method constant, and no row without one.
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

func TestRegistryValidation(t *testing.T) {
	if err := Validate(Methods(), BoxActions()); err != nil {
		t.Fatal(err)
	}
	duplicate := append(Methods(), Methods()[0])
	if err := Validate(duplicate, BoxActions()); err == nil {
		t.Fatal("duplicate method row accepted")
	}
	zero := Methods()
	zero[0].Access = 0
	if err := Validate(zero, BoxActions()); err == nil {
		t.Fatal("zero access accepted")
	}
	noCLI := Methods()
	for i := range noCLI {
		if noCLI[i].Method == contract.MethodPost {
			noCLI[i].Actions[0].CLI = nil
		}
	}
	if err := Validate(noCLI, BoxActions()); err == nil {
		t.Fatal("action without CLI accepted")
	}
	conflict := Methods()
	for i := range conflict {
		if conflict[i].Method == contract.MethodPost {
			conflict[i].Actions[1].Request = reflect.TypeFor[struct {
				Text bool `json:"text"`
			}]()
		}
	}
	if err := Validate(conflict, BoxActions()); err == nil {
		t.Fatal("conflicting field accepted")
	}
	box := BoxActions()
	box[0].Handler = NoBoxHandler
	if err := Validate(Methods(), box); err == nil {
		t.Fatal("box action without handler accepted")
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
	// session/restart and room/repos stay owner-only until the authorization PR opens them.
	required := map[string]string{
		"post": "send update_visual", "session": "start list stop close set_profile move", "room": "log pause", "op": "withdraw", "hostexec": "run request_grant", "pr": "open", "visual": "check", "computer": "open click type text screenshot", "context": "show",
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
