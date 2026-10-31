package room

import (
	"reflect"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestCloseAndMove(t *testing.T) {
	tree := NewTree([]Node{{ID: "root", Host: "h"}, {ID: "a", Parent: "root", Host: "h"}, {ID: "b", Parent: "a", Host: "h"}, {ID: "c", Parent: "b", Host: "h"}, {ID: "peer", Parent: "root", Host: "h"}, {ID: "remote", Parent: "root", Host: "else"}})
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
		parent  contract.SessionID
		fail    bool
	}{
		{"valid", "b", "peer", false},
		{"cycle", "a", "c", true},
		{"self", "a", "a", true},
		{"other room", "a", "outside", true},
		{"other host", "a", "remote", true},
		{"root", "root", "peer", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tree.CheckMove(tt.session, tt.parent)
			if (err != nil) != tt.fail {
				t.Fatalf("got %v", err)
			}
		})
	}
}
