package engine

import (
	"database/sql"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestCreateCompletionPreservesCompletedTurnReset(t *testing.T) {
	s, room, sid, ctx := setup(t)
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET state='creating', image='', failures=2 WHERE id=?", session.Box)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	creating, err := s.store.Box(ctx, session.Box)
	if err != nil {
		t.Fatal(err)
	}
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: sid}
	if _, problem := s.BoxHello(ctx, caller, contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	started, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
	if err != nil {
		t.Fatal(err)
	}
	turn, inputs, _, err := s.store.Dispatch(ctx, sid, "completed", started, false)
	if err != nil || turn == "" || len(inputs) == 0 {
		t.Fatalf("dispatch: turn=%q inputs=%+v err=%v", turn, inputs, err)
	}
	if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: turn, Outcome: contract.TurnCompleted}); problem != nil {
		t.Fatal(problem)
	}
	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.BoxChanged{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.FinishCreate(ctx, creating, "image", "", time.Time{}, 2, event); err != nil {
		t.Fatal(err)
	}
	var failures int
	var state contract.BoxState
	var image string
	if err := s.store.Read().QueryRowContext(ctx, "SELECT state,image,failures FROM boxes WHERE id=?", session.Box).Scan(&state, &image, &failures); err != nil {
		t.Fatal(err)
	}
	if state != contract.BoxReady || image != "image" || failures != 0 {
		t.Fatalf("create completion: state=%s image=%q failures=%d", state, image, failures)
	}
}
