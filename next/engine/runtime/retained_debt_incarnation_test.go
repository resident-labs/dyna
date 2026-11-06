package runtime

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// An assertion probe: the old retained key must not cover runnable resources
// created by a successful same-ID owner restart and later recorded as debt.
func TestRetainedDebtCacheDoesNotCoverRestartedIncarnation(t *testing.T) {
	f := newRestartFixture(t)
	ctx := context.Background()
	f.svc.Reconciler().SetRoomHosts(f.runtime)
	f.exec(t, `DELETE FROM turns; UPDATE boxes SET want='run' WHERE id='b'`)
	if err := f.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := f.st.AppendEvent(tx, contract.Event{ID: "independent-initial-retry", Room: "r", Time: f.now.Add(-time.Minute), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{Box: "b", Session: "s", To: contract.BoxPending}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	pass := func() {
		t.Helper()
		if err := f.svc.Reconciler().Pass(ctx); err != nil {
			t.Fatal(err)
		}
		f.svc.Reconciler().Wait()
	}
	pass() // container-only cleanup caches the original {old,r,b,""} debt
	if problem := f.restart(ctx); problem != nil {
		t.Fatal(problem)
	}
	// Model admission of the fresh incarnation on old, then use real FinishCreate
	// to record another terminal create failure and its automatic other-host debt.
	// No intervening Pass observed SessionRestart's temporary absence of old debt.
	f.exec(t, `UPDATE boxes SET host='old',state='creating',want='run',image='',reason='',updated_at=updated_at+1 WHERE id='b'; UPDATE hosts SET state='unreachable' WHERE name='new'`)
	fresh, err := f.st.Box(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.FinishCreate(ctx, fresh, "", "new creation failed", f.now, 3, contract.Event{ID: "independent-new-create-failed", Room: "r", Time: f.now.Add(time.Second), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{Cause: contract.CauseCreateFailed, Phase: contract.CreatePhaseBox}}, true); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(f.log)
	if err != nil {
		t.Fatal(err)
	}
	// The new automatic retry detached the current host, so old is genuinely
	// eligible for runnable-resource cleanup, not an active-current no-op.
	pass()
	after, err := os.ReadFile(f.log)
	if err != nil {
		t.Fatal(err)
	}
	debts, err := f.st.BoxLeftovers(ctx, "b")
	if err != nil || len(debts) != 1 {
		t.Fatalf("lost debt: %+v %v", debts, err)
	}
	if string(after) == string(before) {
		t.Fatalf("new same-ID incarnation debt suppressed by old retained cache: no host RPC; commands before/after:\n%s", after)
	}
}

// Incomplete may mean 'active current incarnation, performed no host work', not
// 'runnable resources deleted'. The completion cache must distinguish the two.
func TestDeferredDebtIsRetriedWhenFailed(t *testing.T) {
	f := newRestartFixture(t)
	ctx := context.Background()
	f.svc.Reconciler().SetRoomHosts(f.runtime)
	f.exec(t, `UPDATE boxes SET state='starting',want='run',due_at=9999999999999 WHERE id='b'`)
	pass := func() {
		t.Helper()
		if err := f.svc.Reconciler().Pass(ctx); err != nil {
			t.Fatal(err)
		}
		f.svc.Reconciler().Wait()
	}
	pass() // runtime no-op: same host and active current incarnation
	f.exec(t, `UPDATE boxes SET state='failed',reason='failed after current no-op',updated_at=updated_at+1,due_at=0 WHERE id='b'`)
	pass()
	raw, err := os.ReadFile(f.log)
	if os.IsNotExist(err) || (err == nil && !strings.Contains(string(raw), "rm --ignore --volumes b")) {
		t.Fatalf("failed current incarnation was never container-cleaned after active-current no-op was cached: log=%q err=%v", raw, err)
	}
	if err != nil {
		t.Fatal(err)
	}
}
