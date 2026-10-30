package room

import (
	"reflect"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestAddRepo(t *testing.T) {
	a := contract.RoomRepo{Repo: "a", Default: true}
	b := contract.RoomRepo{Repo: "b"}
	for _, tt := range []struct {
		name        string
		have        []contract.RoomRepo
		defaultRoom contract.RoomID
		repo        contract.RepoName
		makeDefault bool
		want        []contract.RoomRepo
		wantRoom    contract.RoomID
	}{
		{"first", nil, "", "a", false, []contract.RoomRepo{a}, "room"},
		{"second", []contract.RoomRepo{a}, "other", "b", false, []contract.RoomRepo{a, b}, "other"},
		{"new default", []contract.RoomRepo{a}, "", "b", true, []contract.RoomRepo{{Repo: "a"}, {Repo: "b", Default: true}}, "room"},
		{"existing default", []contract.RoomRepo{a, b}, "other", "b", true, []contract.RoomRepo{{Repo: "a"}, {Repo: "b", Default: true}}, "other"},
		{"existing unchanged", []contract.RoomRepo{a, b}, "other", "b", false, []contract.RoomRepo{a, b}, "other"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := append([]contract.RoomRepo(nil), tt.have...)
			got, gotRoom := AddRepo("room", tt.have, tt.defaultRoom, tt.repo, tt.makeDefault)
			if !reflect.DeepEqual(got, tt.want) || gotRoom != tt.wantRoom || !reflect.DeepEqual(before, tt.have) {
				t.Fatalf("got %v, %q; input %v", got, gotRoom, tt.have)
			}
		})
	}
}

func TestRepoForSession(t *testing.T) {
	rows := []contract.RoomRepo{{Repo: "a", Default: true}, {Repo: "b"}}
	for _, tt := range []struct {
		name                    string
		requested, parent, want contract.RepoName
		fail                    bool
	}{
		{"default", "", "", "a", false},
		{"inherit", "", "b", "b", false},
		{"override", "a", "b", "a", false},
		{"unknown request", "c", "a", "", true},
		{"unknown parent", "", "c", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RepoForSession(rows, tt.requested, tt.parent)
			if got != tt.want || (err != nil) != tt.fail {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}
