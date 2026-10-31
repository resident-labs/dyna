package tools

import (
	"reflect"
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
