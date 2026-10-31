package store

import (
	"context"
	"testing"
)

func TestProxyAuthPersistsReplacementAndDeletion(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	first := ProxyAuth{ID: "invented", Provider: "fake", Record: []byte("first"), UpdatedAt: 1}
	if err := s.SaveProxyAuth(ctx, first, true); err != nil {
		t.Fatal(err)
	}
	first.Record = []byte("replacement")
	first.UpdatedAt = 2
	if err := s.SaveProxyAuth(ctx, first, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.ListProxyAuths(ctx)
	if err != nil || len(got) != 1 || string(got[0].Record) != "replacement" || got[0].UpdatedAt != 2 {
		t.Fatalf("replacement not persisted: %v", err)
	}
	if err := s.DeleteProxyAuth(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	got, err = s.ListProxyAuths(ctx)
	if err != nil || len(got) != 0 {
		t.Fatalf("deletion not persisted: %v", err)
	}
}
