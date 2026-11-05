package engine

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestSessionStopAcceptsPendingCreatingAndStarting(t *testing.T) {
	s, _, worker, ctx := setup(t)
	for _, state := range []contract.BoxState{contract.BoxPending, contract.BoxCreating, contract.BoxStarting} {
		t.Run(string(state), func(t *testing.T) {
			if err := s.store.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, "UPDATE boxes SET state=?,want='run',due_at=0 WHERE session=?", state, worker)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				answer, problem := s.SessionStop(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStopRequest{Session: worker})
				if problem != nil || !answer.Stopping {
					t.Fatalf("stop %s: %+v %v", state, answer, problem)
				}
			}
			var want string
			if err := s.store.Read().QueryRowContext(ctx, "SELECT want FROM boxes WHERE session=?", worker).Scan(&want); err != nil || want != "stop" {
				t.Fatalf("want %q: %v", want, err)
			}
		})
	}
}

func TestListAndContextExposeTheSameTypedStart(t *testing.T) {
	r := newGrantRig(t)
	ctx := context.Background()
	if err := r.s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET state='creating',want='run' WHERE session=?", r.worker.Session)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	start := contract.BoxStartStatus{Host: "arch", Step: contract.StartBinary, StartedAt: r.s.now().Add(-3 * time.Minute)}
	r.s.SetStartStatus(func(contract.BoxID) *contract.BoxStartStatus { return &start })
	list, problem := r.s.SessionList(ctx, r.owner, contract.SessionListRequest{Room: r.room})
	if problem != nil {
		t.Fatal(problem)
	}
	found := false
	for _, row := range list.Sessions {
		if row.ID == r.worker.Session {
			found = true
			if row.Start == nil || *row.Start != start {
				t.Fatalf("typed list: %+v", row.Start)
			}
		}
	}
	if !found {
		t.Fatal("worker missing")
	}
	context, problem := r.s.ContextSnapshot(ctx, r.root, contract.ContextSnapshotRequest{Section: contract.ContextChildren})
	if problem != nil {
		t.Fatal(problem)
	}
	if len(context.Sections) != 1 || !strings.Contains(context.Sections[0].Text, start.Label(r.s.now())) {
		t.Fatalf("context: %+v", context.Sections)
	}
}
