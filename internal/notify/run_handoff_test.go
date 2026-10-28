package notify

import (
	"reflect"
	"testing"

	"dyna-agent/internal/runstore/runstoretest"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/store"
	"dyna-agent/internal/worktrees"
)

func TestRunCompletionTargetsCurrentOwnerOnce(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	original := e.start(rec, "original", nil)
	next := e.start(rec, "next", nil)
	run := e.newRun("wf_handoff", rec, &original)
	run.Meta.Detached = true
	runstoretest.Write(t, store.DataDir(), run.Meta)
	if err := worktrees.Migrate(store.DataDir()); err != nil {
		t.Fatal(err)
	}
	moved, err := shells.DefaultStore().HandoffRunOwner(e.ctx, run.Meta.ID, original.ID, next.ID, worktrees.HandoffRunTx)
	if err != nil || !moved {
		t.Fatalf("handoff = %v, %v", moved, err)
	}
	if err := run.Finish("ok", "null", nil); err != nil {
		t.Fatal(err)
	}
	result, err := e.svc.CompleteRun(e.ctx, run.Meta.ID)
	if err != nil || !reflect.DeepEqual(result.Message.To, []string{next.Address()}) {
		t.Fatalf("completion = %+v, %v", result.Message, err)
	}
	again, err := e.svc.CompleteRun(e.ctx, run.Meta.ID)
	if err != nil || again.Message.ID != result.Message.ID {
		t.Fatalf("duplicate = %+v, %v", again.Message, err)
	}
}
