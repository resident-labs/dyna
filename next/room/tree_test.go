package room

import (
	"reflect"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestTree(t *testing.T) {
	if _, err := NewTree(nil).ParentFor(contract.Principal{Kind: contract.PrincipalOwner}, ""); err == nil || err.Error() != "room has no root" {
		t.Fatalf("empty tree: %v", err)
	}
	tree := NewTree([]Node{{ID: "root"}, {ID: "a", Parent: "root"}, {ID: "b", Parent: "a"}, {ID: "c", Parent: "root"}})
	for _, tt := range []struct{ order, want []contract.SessionID }{{[]contract.SessionID{"b", "a", "c", "root"}, []contract.SessionID{"b", "c"}}, {[]contract.SessionID{"a", "b"}, []contract.SessionID{"b"}}} {
		if got := tree.Leaves(tt.order); !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("leaves: %v, want %v", got, tt.want)
		}
	}
	// LEGACY(v1-transfer): this block, a tree with several parentless sessions from an M1 trial store. Remove after the transfer.
	legacy := NewTree([]Node{{ID: "first"}, {ID: "second"}})
	parent, err := legacy.ParentFor(contract.Principal{Kind: contract.PrincipalOwner}, "")
	if err != nil || parent != "first" || !legacy.Above("first", "second") || !legacy.HoldsGoals("second") {
		t.Fatalf("legacy root %q: %v", parent, err)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	a := contract.Principal{Kind: contract.PrincipalSession, Session: "a"}
	for _, tt := range []struct {
		name            string
		caller          contract.Principal
		requested, want contract.SessionID
		fail            bool
	}{
		{"owner default", owner, "", "root", false},
		{"owner choice", owner, "a", "a", false},
		{"session", a, "c", "a", false},
		{"missing parent", owner, "missing", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tree.ParentFor(tt.caller, tt.requested)
			if got != tt.want || (err != nil) != tt.fail {
				t.Fatalf("got %s, %v", got, err)
			}
		})
	}
	for _, tt := range []struct {
		name      string
		got, want bool
	}{
		{"ancestor", tree.Above("root", "b"), true},
		{"parent", tree.Above("a", "b"), true},
		{"self", tree.Above("a", "a"), false},
		{"sibling", tree.Above("a", "c"), false},
		{"unknown", tree.Above("root", "missing"), false},
		{"owner manages", tree.CanManage(owner, "root"), true},
		{"owner unknown", tree.CanManage(owner, "missing"), false},
		{"ancestor manages", tree.CanManage(a, "b"), true},
		{"peer cannot manage", tree.CanManage(a, "c"), false},
		{"root no goals", tree.HoldsGoals("root"), false},
		{"child holds goals", tree.HoldsGoals("a"), true},
		{"unknown no goals", tree.HoldsGoals("missing"), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("got %v", tt.got)
			}
		})
	}
}
