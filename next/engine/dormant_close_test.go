package engine

import (
	"database/sql"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestDormantCloseChildBeforeParent(t *testing.T) {
	s, room, parent, ctx := setup(t)
	child, problem := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Parent: parent, Name: "child", Profile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	if _, problem := s.GoalDone(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.GoalDoneRequest{Room: room, Goal: "g1", Summary: "finished"}); problem != nil {
		t.Fatal(problem)
	}
	s.now = func() time.Time { return time.Unix(1000000, 0) }
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE deliveries SET state='read' WHERE to_id IN (?,?)", parent, child.Session); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET state='stopped',want='stop',updated_at=0 WHERE session IN (?,?)", parent, child.Session)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseDormant(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := s.store.Read().QueryRowContext(ctx, "SELECT state FROM sessions WHERE id=?", parent).Scan(&state); err != nil || state != "open" {
		t.Fatalf("parent: %s %v", state, err)
	}
	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.SessionClosed{Session: child.Session, Reason: contract.SessionCloseDormant})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.CloseSession(ctx, child.Session, event); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseDormant(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Read().QueryRowContext(ctx, "SELECT state FROM sessions WHERE id=?", parent).Scan(&state); err != nil || state != "closing" {
		t.Fatalf("parent: %s %v", state, err)
	}
}
