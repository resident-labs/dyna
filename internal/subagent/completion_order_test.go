package subagent

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
)

// The real runner must save recovery intent and release its lock before
// completion can make a parent turn runnable.
func TestTurnCompletionFollowsTerminalIntentAndLockRelease(t *testing.T) {
	e := newTestEnv(t)
	var reports atomic.Int32
	observed := make(chan error, 1)
	e.m.TurnReporter = func(ctx context.Context, manager *Manager, id string, n int) error {
		reports.Add(1)
		check := func() error {
			if _, bounded := ctx.Deadline(); !bounded {
				return fmt.Errorf("completion has no terminal deadline")
			}
			if manager.Store.lockHeld(id) {
				return fmt.Errorf("completion ran before turn lock release")
			}
			turn, err := manager.Store.ReadTurn(id, n)
			if err != nil {
				return err
			}
			if !turn.Done() || !turn.ReportPending {
				return fmt.Errorf("completion has no durable terminal intent: %+v", turn)
			}
			events, err := manager.Store.Events(id)
			if err != nil {
				return err
			}
			for _, event := range events {
				if event["t"] == "turn_end" && event["turn"] == float64(n) {
					return nil
				}
			}
			return fmt.Errorf("completion ran before its turn_end event")
		}
		err := check()
		select {
		case observed <- err:
		default:
		}
		return err
	}
	info, _ := e.start("SLEEP:0\nRESPOND: ready")
	e.wait(info.ID, 1)
	select {
	case err := <-observed:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("the joined runner did not report completion")
	}
	if got := reports.Load(); got != 1 {
		t.Fatalf("completion ran %d times; want once", got)
	}
}
