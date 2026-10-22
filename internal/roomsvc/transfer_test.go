package roomsvc

import (
	"context"
	"testing"

	"dyna-agent/internal/subagent"
)

func TestTransferIsRoomScopedAndKeepsSessionReadable(t *testing.T) {
	e := newEnv(t)
	alpha, beta := e.room("alpha"), e.room("beta")
	info := e.start(alpha, "worker", nil)
	e.mgr.TransferLaunch = func(*subagent.Manager, string, string) error { return nil }
	opts := subagent.TransferOptions{Profile: "mock", WaitSeconds: 30}
	if _, err := e.svc.TransferSession(context.Background(), beta.ID, info.ID, opts); err == nil {
		t.Fatal("cross-room transfer")
	}
	if _, err := e.svc.SessionTransfer(beta.ID, info.ID); err == nil {
		t.Fatal("cross-room status")
	}
	state, err := e.svc.TransferSession(context.Background(), alpha.ID, info.ID, opts)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := e.svc.SessionTransfer(alpha.ID, info.ID); err != nil || status.ID != state.ID {
		t.Fatalf("status: %+v %v", status, err)
	}
	if _, err := e.svc.CancelSessionTransfer(context.Background(), beta.ID, info.ID); err == nil {
		t.Fatal("cross-room cancel")
	}
	if got, err := e.svc.CancelSessionTransfer(context.Background(), alpha.ID, info.ID); err != nil || got.Phase != "cancelled" {
		t.Fatalf("cancel: %+v %v", got, err)
	}
	if old, err := e.svc.TranscriptTurn(info.ID, 1); err != nil || len(old.Turns) != 1 || old.Turns[0].Profile != "mock" || old.Turns[0].HarnessSession == "" {
		t.Fatalf("old transcript: %+v %v", old, err)
	}
}
