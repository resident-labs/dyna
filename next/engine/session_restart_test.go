package engine

import (
	"database/sql"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestSessionRestartOwnerAndState(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	req := contract.SessionRestartRequest{Session: root}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, problem := s.SessionRestart(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: root}, req); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("session caller: %v", problem)
	}
	if _, problem := s.SessionRestart(ctx, owner, req); problem == nil || problem.Code != contract.ErrConflict {
		t.Fatalf("nonfailed box: %v", problem)
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE boxes SET state='failed',reason='failure' WHERE session=?`, root)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, problem := s.SessionRestart(ctx, owner, req); problem != nil {
		t.Fatalf("failed root: %v", problem)
	}
	if _, problem := s.SessionRestart(ctx, owner, req); problem == nil || problem.Code != contract.ErrConflict {
		t.Fatalf("repeated restart: %v", problem)
	}
	events, err := s.store.Events(ctx, room, 0, 100, []contract.EventType{"box.changed"})
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	body := last.Body.(contract.BoxChanged)
	if last.From.Kind != contract.PrincipalOwner || body.Session != root || body.From != contract.BoxFailed || body.To != contract.BoxPending {
		t.Fatalf("restart event: %+v", last)
	}
}
