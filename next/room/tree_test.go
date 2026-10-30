package room

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestTree(t *testing.T) {
	tree := NewTree([]Node{{ID: "root"}, {ID: "a", Parent: "root"}, {ID: "b", Parent: "a"}, {ID: "c", Parent: "root"}})
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
