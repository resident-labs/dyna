package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestRoomRepos(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, id := range []contract.RoomID{"a", "b"} {
		if err := s.CreateRoom(ctx, id, string(id), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	for _, step := range []struct {
		id   contract.RoomID
		repo contract.RepoName
		def  bool
	}{{"a", "one", false}, {"a", "two", true}, {"b", "one", false}} {
		if err := s.AddRoomRepo(ctx, step.id, step.repo, step.def); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.RoomRepos(ctx, "a")
	want := []contract.RoomRepo{{Repo: "one"}, {Repo: "two", Default: true}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, %v", got, err)
	}
	var defaultRoom contract.RoomID
	if err := s.Read().QueryRowContext(ctx, "SELECT default_room FROM repos WHERE repo='one'").Scan(&defaultRoom); err != nil || defaultRoom != "a" {
		t.Fatalf("default room %q: %v", defaultRoom, err)
	}
	if err := s.AddRoomRepo(ctx, "missing", "three", false); err == nil {
		t.Fatal("accepted missing room")
	}
}
