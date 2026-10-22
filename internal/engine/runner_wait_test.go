package engine

import (
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/hub"
	"net"
	"testing"
	"time"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

func TestRunnerWaitInputIsReadOnlyAndNotRecovered(t *testing.T) {
	var wait *websvc.Binding
	for _, binding := range RunnerBindings(func() *subagent.Manager { return nil }) {
		if binding.Name == "RunnerWaitInput" {
			wait = &binding
			break
		}
	}
	if wait == nil {
		t.Fatal("runner has no read-only input wait")
	}
	if wait.Access != websvc.Read || !wait.Local || wait.Scope != websvc.ScopeRead {
		t.Fatalf("wait binding = %+v; want private read-only session wait", wait)
	}
	if runnerCalls()[wait.Name] {
		t.Fatal("read-only wait entered the runner mutation recovery table")
	}
	spool, err := newRunnerSpool(t.TempDir(), func() *subagent.Manager { return nil }, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if spool.calls[wait.Name] != nil {
		t.Fatal("read-only wait entered the offline spool dispatcher")
	}

	if got := callspec.Deadline(wait.Name); got <= 30*time.Second {
		t.Fatalf("wait transport deadline = %v; want more than 30 seconds", got)
	}
	if got := callspec.Deadline("RunnerClaimInput"); got != 5*time.Second {
		t.Fatalf("short claim deadline changed to %v", got)
	}
}

func TestRunnerWaitInputSocketKeepsNoReceipt(t *testing.T) {
	f := newEmptyClaimFixture(t)
	f.manager.InputEvents = hub.New(1)
	calls, err := websvc.Bind(RunnerBindings(func() *subagent.Manager { return f.manager }))
	if err != nil {
		t.Fatal(err)
	}
	applyOnce(calls, f.w, t.Logf)
	callers := &socketCallers{sessions: f.manager.Store}
	callers.actForCallers(calls)
	path := enginetest.ServeCallers(t, f.dir, func(net.Conn) (websvc.Caller, error) {
		return websvc.Caller{Kind: websvc.Session, Session: f.meta.ID}, nil
	}, calls)
	client := engineclient.New(path)
	msg := f.post(t)
	input := subagent.InputClaim{Turn: 1, Depth: f.meta.Depth}
	before := f.w.Health().Writes
	var ready bool
	if err := client.Call(t.Context(), "RunnerWaitInput", []any{input}, &ready); err != nil || !ready {
		t.Fatalf("socket wait = %v, %v", ready, err)
	}
	if n := f.w.Health().Writes - before; n != 0 {
		t.Fatalf("wait wrote %d times", n)
	}
	var receipts int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM request_ids").Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("wait receipts = %d, %v", receipts, err)
	}
	// A supplied ID cannot bypass the live caller and current-turn check by
	// returning an old reply. This read endpoint never accepts replay IDs.
	rid := f.meta.ID + "/forged-wait"
	if err := f.w.FinishRequest(t.Context(), requestKey(t.Context(), rid), "RunnerWaitInput", []byte("true")); err != nil {
		t.Fatal(err)
	}
	before = f.w.Health().Writes
	if err := client.Call(callspec.WithRequest(t.Context(), rid), "RunnerWaitInput", []any{input}, &ready); err == nil {
		t.Fatal("wait accepted a replay ID")
	}
	if f.w.Health().Writes != before {
		t.Fatal("refused wait saved a receipt")
	}
	var claimed subagent.ClaimedInput
	inputID := f.meta.ID + "/after-readiness"
	if err := client.Call(callspec.WithRequest(t.Context(), inputID), "RunnerClaimInput", []any{input}, &claimed); err != nil || len(claimed.Posts) != 1 || claimed.Posts[0].Steer.ID != msg.ID {
		t.Fatalf("claim after readiness = %+v, %v", claimed, err)
	}
	if _, done, err := f.w.RequestDone(t.Context(), runnerRequestKey(inputID)); err != nil || !done {
		t.Fatalf("input receipt = %v, %v", done, err)
	}
}
