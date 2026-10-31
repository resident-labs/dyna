package engine

import (
	"context"
	"database/sql"
	"testing"
	"testing/synctest"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
)

type orderedRecreateHost struct {
	fakeBoxes
	parent        contract.BoxID
	child         contract.BoxID
	parentStarted chan struct{}
	releaseParent chan struct{}
	childStarted  chan struct{}
}

func (h *orderedRecreateHost) Recreate(_ context.Context, spec lifecycle.BoxSpec, _, _ string) error {
	switch spec.Box {
	case h.parent:
		close(h.parentStarted)
		<-h.releaseParent
	case h.child:
		close(h.childStarted)
	}
	return nil
}

func TestReconcilerRecreatesMovedParentBeforeChild(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, room, lead, ctx := setup(t)
		owner := contract.Principal{Kind: contract.PrincipalOwner}
		pass := func(r *lifecycle.Reconciler) {
			t.Helper()
			if err := r.Pass(ctx); err != nil {
				t.Fatal(err)
			}
			r.Wait()
		}
		hello := func(id contract.SessionID) {
			t.Helper()
			if _, problem := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: id}, contract.BoxHelloRequest{}); problem != nil {
				t.Fatal(problem)
			}
		}
		start := func(name string, parent contract.SessionID) contract.SessionStartResponse {
			t.Helper()
			result, problem := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Parent: parent, Name: name, Profile: "test"})
			if problem != nil {
				t.Fatal(problem)
			}
			pass(s.Reconciler())
			hello(result.Session)
			return result
		}
		pass(s.Reconciler())
		hello(lead)
		parent := start("moving", lead)
		child := start("child", parent.Session)
		destination := start("destination", "")
		if _, problem := s.SessionMove(ctx, owner, contract.SessionMoveRequest{Session: parent.Session, NewParent: destination.Session}); problem != nil {
			t.Fatal(problem)
		}
		pass(s.Reconciler())
		_, problem := s.Post(ctx, owner, contract.PostRequest{Room: room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: child.Session}, Text: "wake"})
		if problem != nil {
			t.Fatal(problem)
		}
		if err := s.store.Write(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "UPDATE boxes SET created_at=1000001 WHERE id=?", parent.Box); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, "UPDATE boxes SET created_at=1000002 WHERE id=?", child.Box)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		host := &orderedRecreateHost{parent: parent.Box, child: child.Box, parentStarted: make(chan struct{}), releaseParent: make(chan struct{}), childStarted: make(chan struct{})}
		r := lifecycle.New(s.store, host, s, nil, s.event, s.now)
		if err := r.Pass(ctx); err != nil {
			t.Fatal(err)
		}
		<-host.parentStarted
		synctest.Wait()
		select {
		case <-host.childStarted:
			close(host.releaseParent)
			r.Wait()
			t.Fatal("child recreation started before parent recreation finished")
		default:
		}
		close(host.releaseParent)
		r.Wait()
		if err := r.Pass(ctx); err != nil {
			t.Fatal(err)
		}
		r.Wait()
		select {
		case <-host.childStarted:
		default:
			t.Fatal("child recreation did not start after parent finished")
		}
	})
}
