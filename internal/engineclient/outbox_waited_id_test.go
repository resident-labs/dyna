package engineclient

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

func TestOutboxBoundedCallReturnsOriginalID(t *testing.T) {
	f := &fakeEngine{results: map[string]json.RawMessage{"Admit": json.RawMessage(`"server-supplied-id"`)}}
	out := NewOutbox(serveFake(t, f), "runner")
	defer closeWithin(t, out)
	out.Send("Before")
	var reply string
	id, err := out.CallResultWithinID(t.Context(), &reply, "Admit")
	if err != nil || id != "runner/2" || reply != "server-supplied-id" {
		t.Fatalf("queued ID = %q, reply = %q, error = %v", id, reply, err)
	}
	if got := f.seen(); !slices.Equal(got, []string{"Before runner/1", "Admit runner/2"}) {
		t.Fatalf("ordinary queue sequence: %v", got)
	}
}

func TestOutboxBoundedCallIDPreservesFailure(t *testing.T) {
	f := &fakeEngine{results: map[string]json.RawMessage{"Admit": json.RawMessage(`17`)}}
	out := NewOutbox(serveFake(t, f), "runner")
	defer closeWithin(t, out)
	reply := "unchanged"
	id, err := out.CallResultWithinID(t.Context(), &reply, "Admit")
	var uncertain *UnacknowledgedError
	if !errors.As(err, &uncertain) || id != "runner/1" || uncertain.RequestID != id || reply != "unchanged" {
		t.Fatalf("decode failure: ID %q, reply %q, error %v", id, reply, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	id, err = out.CallResultWithinID(ctx, &reply, "Unsent")
	if id != "" || !errors.Is(err, context.Canceled) || reply != "unchanged" {
		t.Fatalf("unsent cancellation: ID %q, reply %q, error %v", id, reply, err)
	}
}
