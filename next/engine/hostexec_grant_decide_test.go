package engine

import (
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
)

type grantRig struct {
	s            *Service
	room         contract.RoomID
	owner, root  contract.Principal
	worker       contract.Principal
	clock        *time.Time
	ask          func() contract.Operation
	grantsOfTest func() int
}

func newGrantRig(t *testing.T) *grantRig {
	t.Helper()
	s, room, workerID, ctx := setup(t)
	var rootID contract.SessionID
	if err := s.store.Read().QueryRowContext(ctx, "SELECT parent FROM sessions WHERE id=?", workerID).Scan(&rootID); err != nil {
		t.Fatal(err)
	}
	clock := s.now()
	s.now = func() time.Time { return clock }
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.grant": s.HostexecGrant()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	r := &grantRig{s: s, room: room, clock: &clock,
		owner:  contract.Principal{Kind: contract.PrincipalOwner},
		root:   contract.Principal{Kind: contract.PrincipalSession, Session: rootID},
		worker: contract.Principal{Kind: contract.PrincipalSession, Session: workerID}}
	r.ask = func() contract.Operation {
		args := contract.HostexecGrantArgs{Room: room, To: r.worker, Hosts: []contract.HostName{"host"}, Reason: "Inspect logs"}
		res, problem := s.OpSubmit(ctx, r.worker, contract.OpSubmitRequest{Operation: contract.Operation{Room: room, Kind: "hostexec.grant", Args: args}})
		if problem != nil || res.Operation.State != contract.OpPending {
			t.Fatalf("submit: %+v %v", res, problem)
		}
		return res.Operation
	}
	r.grantsOfTest = func() int {
		g, err := s.store.ListActiveGrants(ctx, room, r.worker, s.now())
		if err != nil {
			t.Fatal(err)
		}
		return len(g)
	}
	return r
}

func TestGrantRequestDoesNotExpire(t *testing.T) {
	r := newGrantRig(t)
	ctx := t.Context()
	op := r.ask()
	if !op.Expires.IsZero() {
		t.Fatalf("expires %v", op.Expires)
	}
	*r.clock = r.clock.Add(1000 * time.Hour)
	if err := r.s.ExpireOperations(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := r.s.store.Operation(ctx, op.ID)
	if err != nil || got.Operation.State != contract.OpPending {
		t.Fatalf("after a long wait: %+v %v", got.Operation, err)
	}
	if _, problem := r.s.OpDecide(ctx, r.owner, contract.OpDecideRequest{ID: op.ID, Approve: true}); problem != nil {
		t.Fatal(problem)
	}
}

func TestRootHoldingHostsDecidesGrant(t *testing.T) {
	r := newGrantRig(t)
	op := r.ask()
	if op.Decider != r.root.Session {
		t.Fatalf("decider: %+v", op)
	}
	if _, problem := r.s.OpDecide(t.Context(), r.worker, contract.OpDecideRequest{ID: op.ID, Approve: true}); problem == nil {
		t.Fatal("self approved")
	}
	if _, problem := r.s.OpDecide(t.Context(), r.root, contract.OpDecideRequest{ID: op.ID, Approve: true}); problem != nil {
		t.Fatal(problem)
	}
	waitForOperation(t, r.s, t.Context(), op.ID, contract.OpDone)
	if r.grantsOfTest() != 1 {
		t.Fatal("missing grant")
	}
}

func TestRootCannotDecideAKindWithoutTheHook(t *testing.T) {
	r := newGrantRig(t)
	ctx := t.Context()
	entry := r.s.HostexecGrant()
	entry.Holds = nil
	if entry.NoExpiry != true {
		t.Fatal("grant entry expires")
	}
	if err := r.s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.grant": entry}, time.Hour); err != nil {
		t.Fatal(err)
	}
	op := r.ask()
	if _, problem := r.s.OpDecide(ctx, r.root, contract.OpDecideRequest{ID: op.ID, Approve: true}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("a kind without the hook: %v", problem)
	}
}

func TestClosingASessionWithdrawsItsGrantRequest(t *testing.T) {
	r := newGrantRig(t)
	ctx := t.Context()
	op := r.ask()
	if err := r.s.store.MarkClosing(ctx, r.worker.Session, false, r.s.now()); err != nil {
		t.Fatal(err)
	}
	got, err := r.s.store.Operation(ctx, op.ID)
	if err != nil || got.Operation.State != contract.OpWithdrawn {
		t.Fatalf("after close: %+v %v", got.Operation, err)
	}
	if _, problem := r.s.OpDecide(ctx, r.owner, contract.OpDecideRequest{ID: op.ID, Approve: true}); problem == nil {
		t.Fatal("approved a withdrawn request")
	}
}

func TestRootOfAnotherRoomIsRefused(t *testing.T) {
	r := newGrantRig(t)
	ctx := t.Context()
	created, e := r.s.RoomCreate(ctx, r.owner, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	tree, err := r.s.store.RoomTree(ctx, created.Room)
	if err != nil {
		t.Fatal(err)
	}
	op := r.ask()
	if _, problem := r.s.OpDecide(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: tree.Root()}, contract.OpDecideRequest{ID: op.ID, Approve: true}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("root of another room: %v", problem)
	}
}

func TestGrantRecordsWhoDecided(t *testing.T) {
	r := newGrantRig(t)
	ctx := t.Context()
	op := r.ask()
	if _, problem := r.s.OpDecide(ctx, r.root, contract.OpDecideRequest{ID: op.ID, Approve: true}); problem != nil {
		t.Fatal(problem)
	}
	waitForOperation(t, r.s, ctx, op.ID, contract.OpDone)
	grants, err := r.s.store.ListActiveGrants(ctx, r.room, r.worker, r.s.now())
	if err != nil || len(grants) != 1 || grants[0].By != r.root {
		t.Fatalf("grant: %+v %v", grants, err)
	}
}
