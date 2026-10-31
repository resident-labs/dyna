package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestConnectEpochSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.InitializeConnectLink(ctx, 2); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)
	if err := s.ConsumeConnectNonce(ctx, 4, "first", now, now.Add(time.Minute)); err != nil {
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
	state, err := s.ConnectLink(ctx)
	if err != nil || !state.Linked || state.Epoch != 4 {
		t.Fatalf("state = %+v, error = %v", state, err)
	}
	if err := s.ConsumeConnectNonce(ctx, 3, "nonce", now, now.Add(time.Minute)); !errors.Is(err, ErrConnectEpoch) {
		t.Fatalf("stale nonce: %v", err)
	}
	if err := s.ConsumeConnectNonce(ctx, 4, "nonce", now, now.Add(time.Minute)); err != nil {
		t.Fatalf("stale request consumed nonce: %v", err)
	}
}
