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
		name      string
		got, want contract.SessionID
	}{
		{"owner default", tree.ParentFor(owner, ""), "root"},
		{"owner choice", tree.ParentFor(owner, "a"), "a"},
		{"session", tree.ParentFor(a, "c"), "a"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("got %s", tt.got)
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
