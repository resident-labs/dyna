package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRemovePoolRefusesRepositoryCIHostRule(t *testing.T) {
	ctx := t.Context()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRoom(ctx, "room", "room", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRoomRepo(ctx, "room", "owner/repo", true); err != nil {
		t.Fatal(err)
	}
	if err := s.AddPool(ctx, Pool{Name: "ci-pool", Max: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRepoCI(ctx, "owner/repo", true, 1, []string{"ci-pool"}); err != nil {
		t.Fatal(err)
	}
	for _, on := range []bool{true, false} {
		if err := s.SetRepoCI(ctx, "owner/repo", on, 0); err != nil {
			t.Fatal(err)
		}
		err := s.RemovePool(ctx, "ci-pool")
		var held *PoolInUseError
		if !errors.As(err, &held) || len(held.Repos) != 1 || held.Repos[0] != "owner/repo" || !strings.Contains(err.Error(), "CI host rule of owner/repo") {
			t.Fatalf("pool with stored CI rule was not refused: %v", err)
		}
	}
	if err := s.SetRepoCI(ctx, "owner/repo", false, 0, []string{}); err != nil {
		t.Fatal(err)
	}
	if err := s.RemovePool(ctx, "ci-pool"); err != nil {
		t.Fatal(err)
	}
}
