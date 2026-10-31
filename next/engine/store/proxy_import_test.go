package store

import (
	"context"
	"testing"
)

func TestProxyImportIsAtomicAndIdempotent(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	record := ImportedProxyAuth{Auth: ProxyAuth{ID: "invented", Provider: "claude", Record: []byte("invented"), UpdatedAt: 1}, AccountID: "account", Provider: "anthropic", Label: "Imported Claude 1"}
	if err := s.ImportProxyAuths(ctx, []ImportedProxyAuth{record, record}); err == nil {
		t.Fatal("duplicate import succeeded")
	}
	got, err := s.ListProxyAuths(ctx)
	if err != nil || len(got) != 0 {
		t.Fatal("failed import left records")
	}
	if err := s.ImportProxyAuths(ctx, []ImportedProxyAuth{record}); err != nil {
		t.Fatal(err)
	}
	if err := s.ImportProxyAuths(ctx, []ImportedProxyAuth{record}); err != nil {
		t.Fatal(err)
	}
	changed := record
	changed.Auth.Record = []byte("changed")
	if err := s.ImportProxyAuths(ctx, []ImportedProxyAuth{changed}); err == nil {
		t.Fatal("changed legacy file would be discarded")
	}
	var count int
	if err := s.Read().QueryRow("SELECT count(*) FROM accounts WHERE label='Imported Claude 1'").Scan(&count); err != nil || count != 1 {
		t.Fatal("imported account missing or duplicated")
	}
}
