package room

import (
	"reflect"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestAddress(t *testing.T) {
	tree := NewTree([]Node{{ID: "root"}, {ID: "a", Parent: "root"}, {ID: "b", Parent: "a"}, {ID: "c", Parent: "a"}})
	names := map[string]contract.SessionID{"bob": "b"}
	for _, tt := range []struct {
		text           string
		want           []contract.SessionID
		group, invalid bool
	}{
		{"@bob", []contract.SessionID{"b"}, false, false},
		{"@other:bob", []contract.SessionID{"b"}, false, false},
		{"@parent", []contract.SessionID{"a"}, false, false},
		{"@children", nil, true, false},
		{"@siblings", []contract.SessionID{"c"}, true, false},
		{"@root", []contract.SessionID{"root"}, false, false},
		{"@missing", nil, false, false},
		{"@", nil, false, true},
		{"@a:", nil, false, true},
		{"bob", nil, false, true},
	} {
		t.Run(tt.text, func(t *testing.T) {
			addr, err := ParseAddress(tt.text)
			if (err != nil) != tt.invalid {
				t.Fatalf("parse: %v", err)
			}
			if err != nil {
				return
			}
			got, group := tree.Resolve("b", addr, names)
			if !reflect.DeepEqual(got, tt.want) || group != tt.group {
				t.Fatalf("got %v group %v", got, group)
			}
		})
	}
}
