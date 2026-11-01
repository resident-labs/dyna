package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func TestRoomRepos(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
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

func TestRepoCISettings(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const id contract.RoomID = "room"
	if err := s.CreateRoom(ctx, id, "room", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRoomRepo(ctx, id, "owner/repo", true); err != nil {
		t.Fatal(err)
	}
	if err := s.AddHost(ctx, box.Host{Name: "pve", SSH: "user@pve", State: box.HostReady}); err != nil {
		t.Fatal(err)
	}
	host := contract.RepoCIHost{Repo: "owner/repo", Host: "pve", Runners: 2, Size: contract.BoxSizeLarge}
	if err := s.SetRepoCI(ctx, "missing/repo", []contract.RepoCIHost{host}); !errors.Is(err, ErrRepoNotFound) {
		t.Fatalf("unknown repository: %v", err)
	}
	if err := s.SetRepoCI(ctx, "owner/repo", []contract.RepoCIHost{host}); err != nil {
		t.Fatal(err)
	}
	got, err := s.CIHosts(ctx)
	host.Room = id
	if err != nil || !reflect.DeepEqual(got, []contract.RepoCIHost{host}) {
		t.Fatalf("%+v %v", got, err)
	}
	if err := s.SetRepoCI(ctx, "owner/repo", nil); err != nil {
		t.Fatal(err)
	}
	if got, err := s.CIHosts(ctx); err != nil || len(got) != 0 {
		t.Fatalf("after github mode: %+v %v", got, err)
	}
}
