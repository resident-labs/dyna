package tools

import (
	"reflect"
	"strings"
	"testing"
)

func TestRegistryValidation(t *testing.T) {
	entries := Entries()
	if err := Validate(entries); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func([]Entry) []Entry{
		func(e []Entry) []Entry { e[0].Access = 0; return e },
		func(e []Entry) []Entry { e[0].CLI = nil; return e },
		func(e []Entry) []Entry { return append(e, e[0]) },
		func(e []Entry) []Entry {
			e[1].Request = reflect.TypeFor[struct {
				Text bool `json:"text"`
			}]()
			return e
		},
	} {
		if err := Validate(mutate(Entries())); err == nil {
			t.Fatal("invalid registry accepted")
		}
	}
	for _, e := range AgentEntries() {
		if e.Access == Owner {
			t.Fatal("owner action exposed")
		}
	}
}

func TestPhaseOneActionCoverage(t *testing.T) {
	required := map[string]string{
		"post": "send update_visual", "session": "start list stop close restart set_profile move profiles", "room": "log repos pause", "rules": "list add edit remove", "op": "list withdraw", "hostexec": "run request_grant", "pr": "open", "visual": "check", "computer": "open click type text screenshot", "context": "show usage",
	}
	found := map[string]bool{}
	for _, entry := range AgentEntries() {
		found[entry.Tool+"/"+entry.Action] = true
	}
	for tool, actions := range required {
		for _, action := range strings.Fields(actions) {
			if !found[tool+"/"+action] {
				t.Errorf("missing %s/%s", tool, action)
			}
		}
	}
}
