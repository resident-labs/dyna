package store

import (
	"context"
	"testing"
)

func TestOwnerTimezoneSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if got, err := s.OwnerTimezone(ctx); err != nil || got != "" {
		t.Fatalf("%q %v", got, err)
	}
	if err := s.SetOwnerTimezone(ctx, "Asia/Tokyo"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, err := s.OwnerTimezone(ctx); err != nil || got != "Asia/Tokyo" {
		t.Fatalf("%q %v", got, err)
	}
}
