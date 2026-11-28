package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAgyAccountsAddListRenameRemove(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first, err := NewAgyAccountID()
	if err != nil {
		t.Fatal(err)
	}
	second, _ := NewAgyAccountID()
	if first == second {
		t.Fatal("ids repeat")
	}
	now := time.UnixMilli(5000)
	if err := s.AddAgyAccount(ctx, second, "later", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAgyAccount(ctx, first, "first", now); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAgyAccount(ctx, first, "dup", now); err == nil {
		t.Fatal("a repeated id was added")
	}
	list, err := s.AgyAccounts(ctx)
	if err != nil || len(list) != 2 || list[0].ID != first || list[1].ID != second || !list[0].Created.Equal(now) {
		t.Fatalf("list %+v %v", list, err)
	}
	if ok, err := s.AgyAccountExists(ctx, first); err != nil || !ok {
		t.Fatalf("exists %v %v", ok, err)
	}
	if err := s.RenameAgyAccount(ctx, first, "renamed"); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameAgyAccount(ctx, "nope", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rename missing: %v", err)
	}
	if err := s.RemoveAgyAccount(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveAgyAccount(ctx, first); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remove twice: %v", err)
	}
	if list, _ := s.AgyAccounts(ctx); len(list) != 1 || list[0].ID != second {
		t.Fatalf("after remove %+v", list)
	}
}
