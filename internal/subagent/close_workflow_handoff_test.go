package subagent

import (
	"strings"
	"testing"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/runstore/runstoretest"
	"dyna-agent/internal/store"
)

func TestCloseHandsLiveWorkflowToHeir(t *testing.T) {
	e := newTestEnv(t)
	heir, holder := completedFixtureSession(t, e), completedFixtureSession(t, e)
	if _, err := e.m.Store.UpdateMeta(holder.ID, func(m *Meta) { m.ParentSession = heir.ID; m.Depth = 2 }); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DYNA_SESSION", holder.ID)
	t.Setenv("DYNA_MODE", "subagent")
	t.Setenv("DYNA_RUN_DETACHED", "1")
	run, err := runstore.Create("work", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DYNA_SESSION", "")
	t.Setenv("DYNA_MODE", "")
	old := run.Meta
	old.OwnerHandoff = false
	runstoretest.Write(t, store.DataDir(), old)
	if _, err := e.m.CloseWithOptions(e.ctx, holder.ID, CloseOptions{Force: true}); err == nil || !strings.Contains(err.Error(), "older coordinator") {
		t.Fatalf("forced close of older coordinator: %v", err)
	}
	info, err := e.m.Store.Info(holder.ID)
	if err != nil || !info.Closed.IsZero() {
		t.Fatalf("owner closed: %+v, %v", info, err)
	}
	runstoretest.Write(t, store.DataDir(), run.Meta)
	if _, err := e.m.Close(e.ctx, holder.ID); err != nil {
		t.Fatal(err)
	}
	got, err := runstore.ReadMeta(run.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "running" || got.ParentSession != heir.ID || got.Session != heir.ID {
		t.Fatalf("run after close = %+v; want running and owned by heir", got)
	}
	if err := run.Finish("ok", "", nil); err != nil {
		t.Fatal(err)
	}
	got, err = runstore.ReadMeta(run.Meta.ID)
	if err != nil || got.ParentSession != heir.ID {
		t.Fatalf("finished run = %+v, %v", got, err)
	}
}
