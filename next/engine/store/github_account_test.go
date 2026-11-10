package store

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestGitHubAccountRepositoryIsolation(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, room := range []contract.RoomID{"work", "personal", "other-work"} {
		if err := s.CreateRoom(ctx, room, string(room), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	account := GitHubIdentity{Login: "work-login", Name: "Work User", Email: "work@example.com", Credential: "opaque-id"}
	for _, room := range []contract.RoomID{"work", "other-work"} {
		if err := s.SetGitHubAccount(ctx, room, account); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddRoomRepo(ctx, "work", "org/repo", true); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRoomRepo(ctx, "personal", "org/repo", false); err == nil {
		t.Fatal("attached across accounts")
	}
	if err := s.AddRoomRepo(ctx, "other-work", "org/repo", false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGitHubAccount(ctx, "work", GitHubIdentity{}); err == nil {
		t.Fatal("changed shared repository account")
	}
	if err := s.SetRepoCI(ctx, "org/repo", true, 1); err == nil {
		t.Fatal("enabled CI with engine account")
	}
	got, err := s.GitHubAccount(ctx, "work")
	if err != nil || got != account {
		t.Fatalf("metadata: %+v %v", got, err)
	}
	room, err := s.RepoRoom(ctx, "org/repo")
	if err != nil || room != "work" {
		t.Fatalf("repository room: %s %v", room, err)
	}
}
