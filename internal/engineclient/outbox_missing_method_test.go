package engineclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestOutboxMissingMethodRefusesWritesAndKeepsSending(t *testing.T) {
	f := &fakeEngine{missing: map[string]bool{"New": true}}
	o := NewOutbox(serveFake(t, f), "run")
	defer closeWithin(t, o)
	o.Logf = t.Logf
	missingCtx, cancelMissing := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancelMissing()
	o.Send("New")
	var remote *RemoteError
	if err := o.Call(missingCtx, "New"); !errors.As(err, &remote) || !remote.NoMethod() {
		t.Fatalf("missing method = %v; want a remote missing-method refusal", err)
	}
	otherCtx, cancelOther := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancelOther()
	if err := o.Call(otherCtx, "Other"); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(f.seen()); got != "[New run/1 New run/2 Other run/3]" {
		t.Fatalf("engine saw %s; missing methods must not redirect later writes", got)
	}
}

func TestOutboxMissingMethodPreservesResultAndNextReply(t *testing.T) {
	f := &fakeEngine{missing: map[string]bool{"Claim": true}, results: map[string]json.RawMessage{"Other": json.RawMessage(`29`)}}
	o := NewOutbox(serveFake(t, f), "run")
	defer closeWithin(t, o)
	missingCtx, cancelMissing := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancelMissing()
	result := 17
	var remote *RemoteError
	if err := o.CallResult(missingCtx, &result, "Claim"); !errors.As(err, &remote) || !remote.NoMethod() {
		t.Fatalf("missing result method = %v; want a remote missing-method refusal", err)
	}
	if result != 17 {
		t.Fatalf("refused result = %d; want unchanged 17", result)
	}
	otherCtx, cancelOther := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancelOther()
	if err := o.CallResult(otherCtx, &result, "Other"); err != nil {
		t.Fatal(err)
	}
	if result != 29 {
		t.Fatalf("next engine result = %d; want 29", result)
	}
	if got := fmt.Sprint(f.seen()); got != "[Claim run/1 Other run/2]" {
		t.Fatalf("engine saw %s; want both methods in request order", got)
	}
}
