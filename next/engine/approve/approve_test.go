package approve

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

type fixture struct {
	s     *Service
	now   time.Time
	class contract.Class
	stale bool
	runs  atomic.Int32
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{now: time.Unix(1000, 0), class: contract.ClassAgent}
	st, err := store.Open(t.TempDir(), func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.CreateRoom(ctx, "room", "room", f.now); err != nil {
		t.Fatal(err)
	}
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		for _, v := range []struct{ id, parent string }{{"root", ""}, {"lead", "root"}, {"worker", "lead"}, {"sibling", "root"}} {
			var parent *string
			if v.parent != "" {
				parent = &v.parent
			}
			if _, err := tx.Exec("INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES(?,?,?,?,?,?,?,?)", v.id, "room", v.id, parent, "test", "pending", "", 1000); err != nil {
				return err
			}
			if _, err := tx.Exec("INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)", "box_"+v.id, v.id, "", "small", "ready", "", "", 1000, 1000); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var ids atomic.Int32
	f.s = &Service{Store: st, Context: ctx, Now: func() time.Time { return f.now }, ID: func(prefix string) (string, error) { return fmt.Sprintf("%s%d", prefix, ids.Add(1)), nil }}
	f.s.Kinds = map[contract.OpKind]Entry{"test.run": {
		Class: func(contract.OpArgs, contract.Principal) contract.Class { return f.class },
		Card:  func(contract.OpArgs, contract.Principal) contract.Card { return contract.Card{Title: "Run"} },
		Check: func(context.Context, contract.OpArgs, contract.Principal) error {
			if f.stale {
				return errors.New("changed")
			}
			return nil
		},
		Execute: func(context.Context, contract.OpArgs, contract.Principal) contract.OpResult {
			f.runs.Add(1)
			return contract.OpResult{}
		},
		Decode: func(json.RawMessage) (contract.OpArgs, error) { return nil, nil },
	}}
	return f
}
func principal(id string) contract.Principal {
	return contract.Principal{Kind: contract.PrincipalSession, Session: contract.SessionID(id)}
}
func (f *fixture) submit(t *testing.T, who contract.Principal) contract.Operation {
	t.Helper()
	op, err := f.s.Submit(context.Background(), who, contract.Operation{Room: "room", Kind: "test.run"})
	if err != nil {
		t.Fatal(err)
	}
	return op
}
func TestOwnerRunsEveryClass(t *testing.T) {
	f := setup(t)
	for _, class := range []contract.Class{contract.ClassOwner, contract.ClassAuto, contract.ClassAgent} {
		f.class = class
		if op := f.submit(t, contract.Principal{Kind: contract.PrincipalOwner}); op.State != contract.OpDone {
			t.Fatal(op.State)
		}
	}
}
func TestAutoRuns(t *testing.T) {
	f := setup(t)
	f.class = contract.ClassAuto
	if op := f.submit(t, principal("worker")); op.State != contract.OpDone {
		t.Fatal(op.State)
	}
}
func TestAgentAncestry(t *testing.T) {
	f := setup(t)
	if op := f.submit(t, principal("root")); op.State != contract.OpDone {
		t.Fatal(op.State)
	}
	op := f.submit(t, principal("worker"))
	if op.State != contract.OpPending {
		t.Fatal(op.State)
	}
	if err := f.s.Decide(context.Background(), principal("sibling"), op.ID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if err := f.s.Decide(context.Background(), principal("lead"), op.ID, false, "no"); err != nil {
		t.Fatal(err)
	}
}
func TestOwnerClassWaits(t *testing.T) {
	f := setup(t)
	f.class = contract.ClassOwner
	op := f.submit(t, principal("root"))
	if op.State != contract.OpPending {
		t.Fatal(op.State)
	}
	if err := f.s.Decide(context.Background(), principal("root"), op.ID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
}
func TestStaleCheckPreventsExecution(t *testing.T) {
	f := setup(t)
	op := f.submit(t, principal("worker"))
	f.stale = true
	if err := f.s.Decide(context.Background(), principal("lead"), op.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	// A caller waits on the notification, not on a timer or polling.
	sub, cancel := f.s.Store.Subscribe()
	defer cancel()
	for {
		rec, err := f.s.Store.Operation(context.Background(), op.ID)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Operation.State == contract.OpFailed {
			break
		}
		<-sub
	}
	if f.runs.Load() != 0 {
		t.Fatal("executor ran")
	}
}
func TestExpiryAndPendingDedup(t *testing.T) {
	f := setup(t)
	who := principal("worker")
	first := f.submit(t, who)
	if got := f.submit(t, who); got.ID != first.ID {
		t.Fatal("duplicate differs")
	}
	f.now = f.now.Add(25 * time.Hour)
	second := f.submit(t, who)
	if second.ID == first.ID {
		t.Fatal("expired operation reused")
	}
	old, err := f.s.Store.Operation(context.Background(), first.ID)
	if err != nil || old.Operation.State != contract.OpExpired {
		t.Fatalf("old: %+v %v", old, err)
	}
}
func TestInterruptedExecutionIsNotRetried(t *testing.T) {
	f := setup(t)
	op := f.submit(t, principal("worker"))
	record, err := f.s.Store.Operation(context.Background(), op.ID)
	if err != nil {
		t.Fatal(err)
	}
	err = f.s.Store.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := f.s.Store.TransitionOperation(tx, record.Operation.ID, contract.OpPending, contract.OpApproved, nil, nil, contract.Event{}, nil)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	record, err = f.s.Store.Operation(context.Background(), op.ID)
	if err != nil || record.Operation.State != contract.OpFailed || f.runs.Load() != 0 {
		t.Fatalf("record %+v err %v", record, err)
	}
}
