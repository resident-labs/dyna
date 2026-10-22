package notify

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/subagent"
)

func TestFailureAlertStaysQueuedDuringParentTransfer(t *testing.T) {
	// Transfer state holds the alert; the mock turn needs no steering hold.
	f := newFixture(t, withImmediateTurns)
	before, _ := f.mgr.Store.ReadMeta(f.lead.ID)
	_, err := f.mgr.Store.UpdateMeta(f.lead.ID, func(meta *subagent.Meta) {
		meta.HarnessSession, meta.Resumable = "", false
		meta.Transfer = &subagent.TransferState{ID: "fixture-transfer", Phase: "starting"}
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.mgr.Store.UpdateTurn(f.decoder.ID, 1, func(turn *subagent.Turn) { turn.Status, turn.Error = subagent.TurnError, "provider unavailable" })
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.svc.CompleteSessionTurn(f.ctx, f.decoder.ID, 1)
	if err != nil || !reflect.DeepEqual(res.Message.To, []string{f.lead.Address(), f.a.Name}) || res.Deliveries[0].Delivery != "session-queued" || res.Deliveries[1].Delivery != "room" {
		t.Fatalf("transferring parent alert: %+v %v", res, err)
	}
	queue, err := f.mgr.Store.SessionQueue(f.svc.Rooms, f.lead.ID)
	if err != nil || len(queue) != 1 || queue[0].ID != res.Message.ID || queue[0].State != "waiting" {
		t.Fatalf("lost transfer-time alert: %+v %v", queue, err)
	}
	_, err = f.mgr.Store.UpdateMeta(f.lead.ID, func(meta *subagent.Meta) {
		meta.HarnessSession, meta.Resumable, meta.Transfer = before.HarnessSession, before.Resumable, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The engine delivers the saved alert after the replacement is ready.
	if _, err = f.mgr.TickSessions(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.wait(f.lead.ID, 2)
	if again, err := f.svc.CompleteSessionTurn(f.ctx, f.decoder.ID, 1); err != nil || again.Message.ID != "" {
		t.Fatalf("duplicate transfer-time alert: %+v %v", again, err)
	}
	f.assertIdleTurns(f.lead, 2)
}

func TestActualManualStopReportsOnlyToOwner(t *testing.T) {
	e := newTestEnv(t)
	rec := e.room()
	parent := e.start(rec, "lead", nil)
	e.as(&parent)
	child, _, err := e.mgr.Start(context.Background(), subagent.StartOptions{Goal: "Finish the test task", Profile: "mock", Name: "child", Dir: t.TempDir(), Task: "RESPOND: working SLEEP:60000"})
	e.as(nil)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := e.mgr.Stop(e.ctx, child.ID)
	if err != nil || turn.Status != subagent.TurnStopped || !turn.StopAsked {
		t.Fatalf("manual stop: %+v %v", turn, err)
	}
	res, err := e.svc.CompleteSessionTurn(e.ctx, child.ID, 1)
	if err != nil || res.Message.Kind != "stopped" || !reflect.DeepEqual(res.Message.To, []string{rec.Name}) {
		t.Fatalf("manual stop report: %+v %v", res, err)
	}
	f := &fixture{testEnv: e}
	f.assertIdleTurns(parent, 1)
}

func TestParentRequestedStopStaysQuiet(t *testing.T) {
	for _, stopper := range []string{"parent", "root"} {
		t.Run(stopper, func(t *testing.T) {
			e := newTestEnv(t)
			rec := e.room()
			opts := subagent.StartOptions{Goal: "Finish the test task", Profile: "mock", Name: "child", Dir: t.TempDir(), Task: "RESPOND: working SLEEP:60000"}
			var recipient subagent.Info
			if stopper == "parent" {
				recipient = e.start(rec, "lead", nil)
				e.as(&recipient)
			} else {
				root, _, err := e.mgr.StartRoot(rec, "RESPOND: ready", "")
				if err != nil {
					t.Fatal(err)
				}
				recipient = root
				opts.Room = rec.Name
			}
			child, _, err := e.mgr.Start(context.Background(), opts)
			e.as(nil)
			if err != nil {
				t.Fatal(err)
			}
			e.as(&recipient)
			turn, err := e.mgr.Stop(e.ctx, child.ID)
			e.as(nil)
			if err != nil || turn.Status != subagent.TurnStopped || !turn.StopAsked {
				t.Fatalf("%s stop: %+v %v", stopper, turn, err)
			}
			res, err := e.svc.CompleteSessionTurn(e.ctx, child.ID, 1)
			if err != nil || res.Message.ID != "" {
				t.Fatalf("%s-requested stop posted: %+v %v", stopper, res.Message, err)
			}
		})
	}
}

func TestStopsOtherThanParentRequestStillReport(t *testing.T) {
	for _, tc := range []struct {
		name, reason, error string
	}{
		{"timeout during recipient stop", "recipient", "context deadline exceeded"},
		{"owner stop", "", "context canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t)
			rec := e.room()
			parent := e.start(rec, "lead", nil)
			child := e.start(rec, "child", &parent)
			_, err := e.mgr.Store.UpdateTurn(child.ID, 1, func(turn *subagent.Turn) {
				turn.Status, turn.Error, turn.StopAsked, turn.StopReason = subagent.TurnStopped, tc.error, true, tc.reason
			})
			if err != nil {
				t.Fatal(err)
			}
			res, err := e.svc.CompleteSessionTurn(e.ctx, child.ID, 1)
			if err != nil || res.Message.ID == "" {
				t.Fatalf("stop was not reported: %+v %v", res.Message, err)
			}
		})
	}
}

func TestPlannedStopReportsStaySilent(t *testing.T) {
	for _, reason := range []string{"transfer", "consolidation", "close"} {
		t.Run(reason, func(t *testing.T) {
			e := newTestEnv(t)
			rec := e.room()
			child := e.start(rec, "child", nil)
			_, err := e.mgr.Store.UpdateTurn(child.ID, 1, func(turn *subagent.Turn) {
				turn.Status = subagent.TurnStopped
				turn.StopAsked = true
				turn.StopReason = reason
				turn.Error = "worker canceled by coordinator"
			})
			if err != nil {
				t.Fatal(err)
			}
			res, err := e.svc.CompleteSessionTurn(e.ctx, child.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if res.Message.ID != "" {
				t.Fatalf("planned stop posted: %+v", res.Message)
			}
			turn, err := e.mgr.Store.ReadTurn(child.ID, 1)
			if err != nil || !turn.Reported {
				t.Fatalf("report not claimed: %+v %v", turn, err)
			}
		})
	}
}

func TestActualCloseDoesNotReportStoppedTurn(t *testing.T) {
	e := newTestEnv(t)
	rec := e.room()
	parent := e.start(rec, "lead", nil)
	e.as(&parent)
	child, _, err := e.mgr.Start(context.Background(), subagent.StartOptions{Goal: "Finish the test task", Profile: "mock", Name: "child", Dir: t.TempDir(), Task: "RESPOND: working SLEEP:60000"})
	e.as(nil)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := e.mgr.Close(e.ctx, child.ID)
	if err != nil || closed.Status != subagent.StatusClosed {
		t.Fatalf("close: %+v: %v", closed, err)
	}
	if err := e.waitForTurn(e.ctx, child.ID, 1); err != nil {
		t.Fatal(err)
	}
	result, err := e.svc.CompleteSessionTurn(e.ctx, child.ID, 1)
	if err != nil || result.Message.ID != "" {
		t.Fatalf("close reported a failure or stop: %+v: %v", result, err)
	}
	turn, err := e.mgr.Store.ReadTurn(child.ID, 1)
	if err != nil || turn.Status != subagent.TurnStopped || !turn.StopAsked || turn.StopReason != "close" || !turn.Reported {
		t.Fatalf("close was not recorded and claimed as planned: %+v: %v", turn, err)
	}
}

func TestReplacementStartupTimeoutReportsFailureToOwner(t *testing.T) {
	e := newTestEnv(t)
	rec := e.room()
	child, _, err := e.mgr.Start(context.Background(), subagent.StartOptions{
		Goal: "Finish the test task", Profile: "mock", Name: "replacement", Room: rec.Name,
		Dir: t.TempDir(), Task: "RESPOND: old task",
	})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(child.ID, 1)
	now := time.Now()
	e.mgr.TransferNow = func() time.Time { return now }
	e.mgr.TransferLaunch = func(*subagent.Manager, string, string) error { return nil }
	e.mgr.TransferSleep = func(context.Context, time.Duration) error {
		now = now.Add(61 * time.Second)
		return nil
	}
	reports := make(chan Result, 1)
	e.mgr.TurnReporter = func(reportCtx context.Context, _ *subagent.Manager, id string, turn int) error {
		res, err := e.svc.CompleteSessionTurn(reportCtx, id, turn)
		reports <- res
		return err
	}
	state, err := e.mgr.TransferContext(context.Background(), child.ID, subagent.TransferOptions{Profile: "mock", WaitSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	// The old task has no hold directive. Only the replacement holds, until
	// the injected startup deadline cancels its real in-process runner.
	if err = e.mgr.SubmitHandoff(context.Background(), child.ID, state.ID, "SLEEP:60000 RESPOND: replacement", "", ""); err != nil {
		t.Fatal(err)
	}
	if err = e.mgr.ProcessTransfer(e.ctx, child.ID, state.ID); err == nil || !strings.Contains(err.Error(), "no model output") {
		t.Fatalf("startup timeout = %v", err)
	}
	select {
	case res := <-reports:
		if res.Message.Kind != "failed" || !reflect.DeepEqual(res.Message.To, []string{rec.Name}) || !strings.Contains(res.Message.Text, "replacement startup produced no model output") {
			t.Fatalf("startup failure report = %+v", res.Message)
		}
	case <-e.ctx.Done():
		t.Fatal("replacement did not report its timeout")
	}
}
