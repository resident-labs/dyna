package main

import (
	"context"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestConnectFixture(t *testing.T) {
	full, err := (devConnect{}).Status(context.Background())
	if err != nil || !full.Linked || full.Attempt.State != contract.ConnectAttemptLinked || full.Relay == "" || full.Fingerprint == "" {
		t.Fatalf("full fixture: %+v, %v", full, err)
	}
	empty, err := (devConnect{empty: true}).Status(context.Background())
	if err != nil || empty.Linked || empty.Attempt.State != contract.ConnectAttemptIdle {
		t.Fatalf("empty fixture: %+v, %v", empty, err)
	}
}
