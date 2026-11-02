package engine

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func (r *goalRig) ownsPR(session contract.SessionID, number int) {
	r.t.Helper()
	r.exec("INSERT OR IGNORE INTO repos(repo,default_room) VALUES('o/r',?)", r.room)
	r.exec("INSERT INTO pr_owners(repo,number,session,goal,approved_head) VALUES('o/r',?,?,'','')", number, session)
}

func TestOwnerAndAncestorsCloseWhateverIsHeldButNobodyElseDoes(t *testing.T) {
	r := newGoalRig(t)
	r.ownsPR(r.child, 7)
	r.askAs(r.child, true)
	r.notice(r.child)
	r.begin(r.child)
	if _, e := r.s.SessionClose(r.ctx, asSession(r.child), contract.SessionCloseRequest{Session: r.child}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("a session closed itself: %v", e)
	}
	if _, e := r.s.SessionClose(r.ctx, asSession(r.lead), contract.SessionCloseRequest{Session: r.child}); e != nil {
		t.Fatalf("the parent was refused: %v", e)
	}
	if _, e := r.s.SessionClose(r.ctx, ownerCaller, contract.SessionCloseRequest{Session: r.root}); e == nil {
		t.Fatal("root closed")
	}
}
