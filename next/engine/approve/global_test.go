package approve

import (
	"context"
	"errors"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestGlobalOwnerOperationOnlyAppearsOnOwnerStream(t *testing.T) {
	f := setup(t)
	entry := f.s.Kinds["test.run"]
	entry.Global = true
	f.s.Kinds["test.run"] = entry
	ctx := context.Background()
	input := contract.Operation{Args: fakeArgs{}}
	if _, err := f.s.Submit(ctx, principal("worker"), input); !errors.Is(err, ErrForbidden) {
		t.Fatal("session could submit global operation")
	}
	op, err := f.s.Submit(ctx, contract.Principal{Kind: contract.PrincipalOwner}, input)
	if err != nil || op.State != contract.OpDone {
		t.Fatalf("owner action failed: %v", err)
	}
	record, err := f.s.Store.Operation(ctx, op.ID)
	if err != nil || record.Operation.Room != "" {
		t.Fatal("global operation was assigned a room")
	}
	events, err := f.s.Store.OwnerEvents(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if _, ok := event.Body.(contract.OpRan); ok && event.Room == "" {
			found = true
		}
	}
	if !found {
		t.Fatal("owner stream did not receive global action")
	}
	events, err = f.s.Store.Events(ctx, "room", 0, 100, []contract.EventType{"op.ran"})
	if err != nil || len(events) != 0 {
		t.Fatal("global action reached a room")
	}
	var count int
	if err := f.s.Store.Read().QueryRow("SELECT count(*) FROM deliveries").Scan(&count); err != nil || count != 0 {
		t.Fatal("global action produced a delivery")
	}
}
