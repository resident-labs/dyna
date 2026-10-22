package subagent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"dyna-agent/internal/mode"
)

func stampParent(t *testing.T, parent Info) {
	t.Helper()
	t.Setenv(mode.EnvMode, mode.Subagent)
	t.Setenv(mode.EnvSession, parent.ID)
	t.Setenv(mode.EnvDepth, "3") // deliberately stale; the store is authoritative
	t.Setenv(mode.EnvRoomID, parent.RoomID)
	t.Setenv(mode.EnvRoom, parent.Room)
}

func TestPromotePreservesHistoryAndDescendants(t *testing.T) {
	e := newTestEnv(t)
	lead, _ := e.start("SLEEP:1 RESPOND: lead")
	lead, _ = e.wait(lead.ID, 1)
	stampParent(t, lead)
	child, _ := e.start("SLEEP:1 RESPOND: child", func(o *StartOptions) { o.Service = true })
	child, _ = e.wait(child.ID, 1)
	stampParent(t, child)
	grandchild, _ := e.start("SLEEP:1 RESPOND: grandchild")
	grandchild, _ = e.wait(grandchild.ID, 1)
	if _, err := e.m.Close(e.ctx, grandchild.ID); err != nil {
		t.Fatal(err)
	}
	before, _ := e.m.Store.ReadMeta(child.ID)
	turnBefore, _ := e.m.Store.ReadTurn(child.ID, 1)
	stampParent(t, lead)
	promoted, err := e.m.Promote(context.Background(), child.Address())
	if err != nil || !promoted.Service || promoted.Depth != 1 || promoted.ParentSession != "" || promoted.Owner != "" || promoted.Status != StatusIdle {
		t.Fatalf("Promote = %+v, %v", promoted, err)
	}
	after, _ := e.m.Store.ReadMeta(child.ID)
	before.ParentSession, before.Owner, before.Depth = "", "", 1
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("promotion changed history: before=%+v after=%+v", before, after)
	}
	turnAfter, _ := e.m.Store.ReadTurn(child.ID, 1)
	if !reflect.DeepEqual(turnBefore, turnAfter) {
		t.Fatal("promotion changed a turn record")
	}
	gc, _ := e.m.Store.Info(grandchild.ID)
	if gc.Depth != 2 || gc.ParentSession != child.ID || gc.Status != StatusClosed {
		t.Fatalf("closed descendant = %+v", gc)
	}
	e.owner()
	if again, err := e.m.Promote(context.Background(), child.ID); err != nil || again.Depth != 1 {
		t.Fatalf("repeat promotion = %+v, %v", again, err)
	}
	stampParent(t, promoted)
	newChild, _ := e.start("SLEEP:1 RESPOND: new child")
	e.wait(newChild.ID, 1)
	if newChild.Depth != 2 || newChild.ParentSession != child.ID {
		t.Fatalf("new child from stale env = %+v", newChild)
	}
}

func TestPromoteRunningSessionAndLiveHierarchy(t *testing.T) {
	e := newTestEnv(t)
	lead, _ := e.start("SLEEP:1 RESPOND: lead")
	lead, _ = e.wait(lead.ID, 1)
	stampParent(t, lead)
	started := make(chan struct{})
	resume := make(chan struct{})
	var startedOnce, resumeOnce sync.Once
	t.Cleanup(func() { resumeOnce.Do(func() { close(resume) }) })
	e.m.checkpoint = func(stage string) {
		if stage == "turn-started" {
			startedOnce.Do(func() { close(started) })
			select {
			case <-resume:
			case <-e.ctx.Done():
			}
		}
	}
	child, _ := e.start("job RESPOND: completed")
	select {
	case <-started:
	case <-e.ctx.Done():
		t.Fatal("mock turn did not start")
	}
	promoted, err := e.m.Promote(context.Background(), child.ID)
	if err != nil || (promoted.Status != StatusRunning && promoted.Status != StatusIdle) || promoted.Turns != child.Turns {
		t.Fatalf("running promotion = %+v, %v", promoted, err)
	}
	resumeOnce.Do(func() { close(resume) })
	// Join the running mock's completion event. Its delivery record includes
	// both the live read and final drain, so a duplicate frame also fails.
	_, completed, err := e.waitTurn(e.ctx, child.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != TurnOK || completed.StopAsked {
		t.Fatalf("promotion interrupted the turn: %+v", completed)
	}
	if len(completed.Steering) != 1 {
		t.Fatalf("live hierarchy: want exactly one frame, got %+v", completed.Steering)
	}
	frame := completed.Steering[0]
	if !frame.Delivered || !strings.Contains(frame.Message, "DYNA HIERARCHY CHANGED") || !strings.Contains(frame.Message, "depth 1") || !strings.Contains(frame.Message, "@parent = @root") {
		t.Fatalf("live hierarchy = %+v", frame)
	}
	if _, err := e.m.Close(e.ctx, child.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPromoteInvalidAndClosed(t *testing.T) {
	e := newTestEnv(t)
	for _, ref := range []string{"", "../session", "ses_missing-xyz"} {
		if _, err := e.m.Promote(context.Background(), ref); err == nil {
			t.Fatalf("accepted %q", ref)
		}
	}
	info, _ := e.start("SLEEP:1 RESPOND: done")
	e.wait(info.ID, 1)
	if _, err := e.m.Close(e.ctx, info.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Promote(context.Background(), info.ID); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed promotion: %v", err)
	}
}

func TestCloseHandsChildrenUpAndKeepsPRGuard(t *testing.T) {
	e := newTestEnv(t)
	lead, _ := e.start("SLEEP:1 RESPOND: lead")
	lead, _ = e.wait(lead.ID, 1)
	stampParent(t, lead)
	child, _ := e.start("SLEEP:1 RESPOND: child")
	child, _ = e.wait(child.ID, 1)
	stampParent(t, child)
	gc, _ := e.start("SLEEP:1 RESPOND: grandchild")
	e.wait(gc.ID, 1)
	writePRLink(t, lead, "author", "open", true)
	if _, err := e.m.CloseWithOptions(e.ctx, lead.ID, CloseOptions{}); err == nil {
		t.Fatal("PR guard bypassed")
	}
	if unchanged, _ := e.m.Store.ReadMeta(child.ID); unchanged.Depth != 2 || unchanged.ParentSession != lead.ID {
		t.Fatal("guard mutated child")
	}
	if _, err := e.m.CloseWithOptions(e.ctx, lead.ID, CloseOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	// The child holds its goal, so it moves up a level with its subtree.
	current, _ := e.m.Store.Info(child.ID)
	grandchild, _ := e.m.Store.Info(gc.ID)
	if current.Status == StatusClosed || grandchild.Status == StatusClosed || current.Depth != 1 || current.ParentSession != "" || grandchild.Depth != 2 || grandchild.ParentSession != child.ID {
		t.Fatalf("handed-over tree: %+v %+v", current, grandchild)
	}
}

func TestPromoteQueuedSessionAndRunningDescendant(t *testing.T) {
	e := newTestEnv(t)
	lead, _ := e.start("SLEEP:1 RESPOND: lead")
	lead, _ = e.wait(lead.ID, 1)
	stampParent(t, lead)
	held := &heldTurnSpawner{}
	e.m.Spawner = held
	child, _ := e.start("SLEEP:1 RESPOND: child")
	defer held.lock.Close()
	if child.Status != StatusQueued {
		t.Fatalf("not queued: %+v", child)
	}
	stampParent(t, child)
	grandchildHeld := &heldTurnSpawner{}
	e.m.Spawner = grandchildHeld
	grandchild, _ := e.start("SLEEP:1 RESPOND: grandchild")
	defer grandchildHeld.lock.Close()
	if _, err := e.m.Store.UpdateTurn(grandchild.ID, 1, func(t *Turn) { t.Status = TurnRunning }); err != nil {
		t.Fatal(err)
	}
	meta, _ := e.m.Store.ReadMeta(grandchild.ID)
	gate := e.m.Store.newTurnGate(e.m.RoomStore(), meta, 1, meta.RoomCursor)
	if err := gate.Activate(); err != nil {
		t.Fatal(err)
	}
	stampParent(t, lead)
	if got, err := e.m.Promote(context.Background(), child.ID); err != nil || got.Status != StatusQueued || got.Depth != 1 {
		t.Fatalf("queued promotion = %+v %v", got, err)
	}
	messages, _, err := gate.Read(0, false)
	if err != nil || len(messages) != 1 || !strings.Contains(messages[0].Message, "depth 2") || !strings.Contains(messages[0].Message, "@parent = "+child.Address()) {
		t.Fatalf("descendant hierarchy frame: %+v %v", messages, err)
	}
	stampParent(t, grandchild)
	deeperHeld := &heldTurnSpawner{}
	e.m.Spawner = deeperHeld
	deeper, _ := e.start("SLEEP:1 RESPOND: deeper")
	defer deeperHeld.lock.Close()
	if deeper.Depth != 3 || deeper.ParentSession != grandchild.ID {
		t.Fatalf("descendant stale launch stamp: %+v", deeper)
	}
}

func TestChildCreationRejectsMissingOrClosedParent(t *testing.T) {
	e := newTestEnv(t)
	parent, _ := e.start("SLEEP:1 RESPOND: done")
	e.wait(parent.ID, 1)
	if _, err := e.m.Close(e.ctx, parent.ID); err != nil {
		t.Fatal(err)
	}
	stampParent(t, parent)
	if _, _, err := e.m.Start(context.Background(), StartOptions{Goal: "Finish the test task", Profile: "mock", Dir: t.TempDir(), Task: "SLEEP:1 RESPOND: invalid"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed parent: %v", err)
	}
	t.Setenv(mode.EnvSession, "ses_missing-xyz")
	if _, _, err := e.m.Start(context.Background(), StartOptions{Goal: "Finish the test task", Profile: "mock", Dir: t.TempDir(), Task: "SLEEP:1 RESPOND: invalid"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing parent: %v", err)
	}
}

func TestPromoteTopLevelIsUnchanged(t *testing.T) {
	e := newTestEnv(t)
	lead, _ := e.start("SLEEP:1 RESPOND: lead")
	e.wait(lead.ID, 1)
	before, err := e.m.Store.UpdateMeta(lead.ID, func(m *Meta) { m.Owner = "codex:original-owner" })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Promote(context.Background(), lead.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := e.m.Store.ReadMeta(lead.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("top-level promotion mutated metadata: %+v %+v", before, after)
	}
}
