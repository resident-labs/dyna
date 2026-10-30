package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestOperationAtomicDedupAndTransition(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	s, err := Open(t.TempDir(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRoom(ctx, "room", "room", now); err != nil {
		t.Fatal(err)
	}
	record := StoredOperation{Operation: contract.Operation{ID: "op", Kind: "test.run", Room: "room", By: contract.Principal{Kind: contract.PrincipalOwner}, Class: contract.ClassAgent, State: contract.OpPending, Created: now, Expires: now.Add(time.Hour), Card: contract.Card{Title: "Run"}}, Args: json.RawMessage(`{"x":1}`)}
	event := contract.Event{ID: "event", Time: now, Room: "room", From: record.Operation.By, Body: contract.PostCreated{Text: "requested"}}
	err = s.Write(ctx, func(tx *sql.Tx) error { return s.InsertOperation(tx, record, event, nil) })
	if err != nil {
		t.Fatal(err)
	}
	err = s.Write(ctx, func(tx *sql.Tx) error {
		found, err := s.PendingOperation(tx, "room", record.Operation.By, record.Operation.Kind, record.Args)
		if err != nil || found.Operation.ID != "op" {
			t.Fatalf("pending: %+v %v", found, err)
		}
		other := record
		other.Operation.ID = "other"
		return s.InsertOperation(tx, other, contract.Event{}, nil)
	})
	if err == nil {
		t.Fatal("duplicate pending operation accepted")
	}
	err = s.Write(ctx, func(tx *sql.Tx) error {
		ok, err := s.TransitionOperation(tx, "op", contract.OpApproved, contract.OpDone, nil, nil, event, nil)
		if err != nil || ok {
			t.Fatalf("incorrect transition: %v %v", ok, err)
		}
		ok, err = s.TransitionOperation(tx, "op", contract.OpPending, contract.OpExpired, nil, nil, event, nil)
		if err == nil || ok {
			t.Fatal("duplicate event must roll back")
		}
		return err
	})
	if err == nil {
		t.Fatal("expected event failure")
	}
	got, err := s.Operation(ctx, "op")
	if err != nil || got.Operation.State != contract.OpPending || string(got.Args) != string(record.Args) {
		t.Fatalf("operation: %+v %v", got, err)
	}
	err = s.Write(ctx, func(tx *sql.Tx) error {
		ok, err := s.TransitionOperation(tx, "op", contract.OpPending, contract.OpExpired, nil, nil, contract.Event{}, nil)
		if !ok || err != nil {
			return errors.New("transition failed")
		}
		record.Operation.ID = "replacement"
		return s.InsertOperation(tx, record, contract.Event{}, nil)
	})
	if err != nil {
		t.Fatal(err)
	}
}
