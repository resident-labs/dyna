package subagent

import (
	"context"
	"errors"
	"testing"
)

func TestCloseContinuesWhenTurnEndsBeforeStop(t *testing.T) {
	for _, status := range []string{TurnRunning, TurnQueued} {
		t.Run(status, func(t *testing.T) {
			e := newTestEnv(t)
			info := completedFixtureSession(t, e)
			if _, err := e.m.Store.UpdateTurn(info.ID, 1, func(turn *Turn) { turn.Status = status }); err != nil {
				t.Fatal(err)
			}
			lock, err := e.m.Store.tryTurnLock(info.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			reached := false
			e.m.checkpoint = func(stage string) {
				if stage != "close-before-stop" {
					return
				}
				reached = true
				if _, err := e.m.Store.UpdateTurn(info.ID, 1, func(turn *Turn) { turn.Status = TurnOK }); err != nil {
					t.Fatal(err)
				}
				if err := lock.Close(); err != nil {
					t.Fatal(err)
				}
			}
			closed, err := e.m.Close(e.ctx, info.ID)
			if !reached {
				t.Fatal("close did not reach the stop race window")
			}
			if err != nil || closed.Status != StatusClosed {
				t.Fatalf("close after turn completed: status=%s, error=%v", closed.Status, err)
			}
			turn, err := e.m.Store.ReadTurn(info.ID, 1)
			if err != nil || turn.Status != TurnOK || turn.StopAsked {
				t.Fatalf("completed turn changed: %+v, error=%v", turn, err)
			}
		})
	}
}

func TestClosePreservesStopCancellation(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	lock, err := e.m.Store.tryTurnLock(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	e.m.checkpoint = func(stage string) {
		if stage == "close-before-stop" {
			cancel()
		}
	}
	if _, err := e.m.Close(ctx, info.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("close error=%v, want context canceled", err)
	}
	meta, err := e.m.Store.ReadMeta(info.ID)
	if err != nil || !meta.Closed.IsZero() {
		t.Fatalf("failed stop closed session: %+v, error=%v", meta, err)
	}
}

func TestStopIdleReturnsNoRunningTurnError(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	_, err := e.m.Stop(e.ctx, info.ID)
	var noRunningTurn *NoRunningTurnError
	if !errors.As(err, &noRunningTurn) || noRunningTurn.SessionID != info.ID || noRunningTurn.Status != StatusIdle {
		t.Fatalf("stop error=%v, want typed idle result for %s", err, info.ID)
	}
}
