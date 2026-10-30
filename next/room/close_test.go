package room

import (
	"reflect"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestCloseAndMove(t *testing.T) {
	tree := NewTree([]Node{{ID: "root", Room: "room", Host: "h"}, {ID: "a", Parent: "root", Room: "room", Host: "h"}, {ID: "b", Parent: "a", Room: "room", Host: "h"}, {ID: "c", Parent: "b", Room: "room", Host: "h"}, {ID: "peer", Parent: "root", Room: "room", Host: "h"}})
	for _, tt := range []struct {
		name    string
		session contract.SessionID
		want    []contract.SessionID
		fail    bool
	}{
		{"subtree", "a", []contract.SessionID{"c", "b", "a"}, false},
		{"leaf", "b", []contract.SessionID{"c", "b"}, false},
		{"root", "root", nil, true},
		{"missing", "missing", nil, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tree.CloseOrder(tt.session)
			if (err != nil) != tt.fail || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v %v", got, err)
			}
		})
	}
	for _, tt := range []struct {
		name    string
		session contract.SessionID
		want    Handoff
		fail    bool
	}{
		{"to root", "a", Handoff{PRHolder: "root"}, false},
		{"to parent", "b", Handoff{PRHolder: "a", GoalHolder: "a"}, false},
		{"root", "root", Handoff{}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tree.Handoff(tt.session)
			if (err != nil) != tt.fail || got != tt.want {
				t.Fatalf("got %+v %v", got, err)
			}
		})
	}
	for _, tt := range []struct {
		name    string
		session contract.SessionID
		parent  Node
		fail    bool
	}{
		{"valid", "b", Node{ID: "peer", Room: "room", Host: "h"}, false},
		{"cycle", "a", Node{ID: "c", Room: "room", Host: "h"}, true},
		{"self", "a", Node{ID: "a", Room: "room", Host: "h"}, true},
		{"other room", "a", Node{ID: "outside", Room: "else", Host: "h"}, true},
		{"other host", "a", Node{ID: "peer", Room: "room", Host: "else"}, true},
		{"root", "root", Node{ID: "peer", Room: "room", Host: "h"}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tree.CheckMove(tt.session, tt.parent)
			if (err != nil) != tt.fail {
				t.Fatalf("got %v", err)
			}
		})
	}
}
