package engine

import (
	"database/sql"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestOwnerPostAfterCancelledStartWakesNormally(t *testing.T) {
	s, room, worker, ctx := setup(t)
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET state='pending',want='run' WHERE session=?", worker)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	post := func(text string) {
		t.Helper()
		if _, problem := s.Post(ctx, owner, contract.PostRequest{Room: room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: worker}, Text: text}); problem != nil {
			t.Fatal(problem)
		}
	}
	post("before startup stop")
	if _, problem := s.SessionStop(ctx, owner, contract.SessionStopRequest{Session: worker}); problem != nil {
		t.Fatal(problem)
	}
	var wakes int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM deliveries WHERE to_id=? AND state='pending' AND wake=1", worker).Scan(&wakes); err != nil || wakes != 0 {
		t.Fatalf("startup stop did not cancel pending wakes: %d %v", wakes, err)
	}
	post("after startup stop")
	channel, problem := testChannel(t, s, ctx, boxCaller(worker))
	if problem != nil {
		t.Fatal(problem)
	}
	select {
	case msg := <-channel:
		if _, ok := msg.Body.(contract.TurnStart); !ok {
			t.Fatalf("expected resumed startup, got %+v", msg.Body)
		}
	case <-time.After(time.Second):
		t.Fatal("new owner post did not resume cancelled start")
	}
}
