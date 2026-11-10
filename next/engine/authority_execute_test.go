package engine

import (
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
	"testing"
	"time"
)

func TestGrantExecutionRechecksRevokedDecider(t *testing.T) {
	r := newGrantRig(t)
	ctx := t.Context()
	entered, release := make(chan struct{}), make(chan struct{})
	entry := r.s.HostexecGrant()
	execute := entry.Execute
	entry.Execute = func(ctx context.Context, id contract.OpID, args contract.OpArgs, who contract.Principal) contract.OpResult {
		close(entered)
		<-release
		return execute(ctx, id, args, who)
	}
	if err := r.s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.grant": entry}, time.Hour); err != nil {
		t.Fatal(err)
	}
	op := r.ask()
	if _, p := r.s.OpDecide(ctx, r.root, contract.OpDecideRequest{ID: op.ID, Approve: true}); p != nil {
		t.Fatal(p)
	}
	<-entered
	if err := r.s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE grants SET revoked_at=1 WHERE to_id=?", r.root.Session)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitForOperation(t, r.s, ctx, op.ID, contract.OpFailed)
	stored, err := r.s.store.Operation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Operation.Result == nil || stored.Operation.Result.Cause != contract.CauseStale {
		t.Fatalf("result %+v", stored.Operation.Result)
	}
	if r.grantsOfTest() > 0 {
		t.Fatal("revoked decider minted worker access")
	}
}

func TestExpiredHolderIsNotADecider(t *testing.T) {
	r := newGrantRig(t)
	ctx := t.Context()
	if err := r.s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE grants SET expires_at=? WHERE to_id=?", r.s.now().Add(-time.Hour).UnixMilli(), r.root.Session)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	op := r.ask()
	if op.Decider == r.root.Session {
		t.Fatal("expired holder selected as decider")
	}
}
