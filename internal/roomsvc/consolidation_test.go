package roomsvc

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/subagent"
)

func TestConsolidationRoomAPIValidatesBeforeReservationAndReopensState(t *testing.T) {
	e := newEnv(t)
	alpha, beta := e.room("alpha"), e.room("beta")
	a, b := e.start(alpha, "a", nil), e.start(alpha, "b", nil)
	e.mgr.ConsolidationLaunch = func(*subagent.Manager, string, string) error { return nil }
	opts := subagent.ConsolidationOptions{Sessions: []string{a.ID, b.ID}, Target: a.ID}
	if _, err := e.svc.ConsolidateSessions(context.Background(), beta.ID, opts); err == nil {
		t.Fatal("cross-room consolidation accepted")
	}
	c, err := e.svc.ConsolidateSessions(context.Background(), alpha.ID, opts)
	if err != nil {
		t.Fatal(err)
	}
	reopened := New(e.svc.Notify)
	states, err := reopened.SessionConsolidations(alpha.ID)
	if err != nil || len(states) != 1 || states[0].ID != c.ID {
		t.Fatalf("reopen: %+v %v", states, err)
	}
	if _, err := reopened.ResumeSessionConsolidation(context.Background(), beta.ID, c.ID); err == nil {
		t.Fatal("cross-room resume accepted")
	}
	if _, err := reopened.CancelSessionConsolidation(context.Background(), beta.ID, c.ID); err == nil {
		t.Fatal("cross-room cancel accepted")
	}
	c, err = reopened.CancelSessionConsolidation(context.Background(), alpha.ID, c.ID)
	if err != nil || c.Phase != "cancelled" {
		t.Fatalf("cancel %+v %v", c, err)
	}
}

// A session caller may transfer, hand off for, consolidate, resume or cancel
// only sessions in its own tree, as the CLI's Target checked before these
// became engine calls. The owner may act on any of them.
func TestTransferAndConsolidationCheckTheCallerSession(t *testing.T) {
	e := newEnv(t)
	alpha := e.room("alpha")
	a, b := e.start(alpha, "a", nil), e.start(alpha, "b", nil)
	stranger := e.start(alpha, "stranger", nil)
	e.mgr.TransferLaunch = func(*subagent.Manager, string, string) error { return nil }
	e.mgr.ConsolidationLaunch = func(*subagent.Manager, string, string) error { return nil }
	as := authority.WithActor(e.ctx, authority.Actor{Role: authority.Session, Session: stranger.ID, Room: alpha.ID})
	owner := authority.WithActor(e.ctx, authority.Actor{Role: authority.Owner})
	refused := func(what string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "may act only on itself and its descendants") {
			t.Errorf("%s by a stranger session = %v; want the descendant refusal", what, err)
		}
	}
	opts := subagent.ConsolidationOptions{Sessions: []string{a.ID, b.ID}, Target: a.ID}
	_, err := e.svc.TransferSession(as, alpha.ID, a.ID, subagent.TransferOptions{Profile: "mock", WaitSeconds: 30})
	refused("transfer", err)
	_, err = e.svc.CancelSessionTransfer(as, alpha.ID, a.ID)
	refused("cancel transfer", err)
	refused("handoff", e.svc.SubmitTransferHandoff(as, alpha.ID, a.ID, "token", "text", "", ""))
	_, err = e.svc.ConsolidateSessions(as, alpha.ID, opts)
	refused("consolidate", err)
	c, err := e.svc.ConsolidateSessions(owner, alpha.ID, opts)
	if err != nil {
		t.Fatalf("the owner could not consolidate: %v", err)
	}
	_, err = e.svc.ResumeSessionConsolidation(as, alpha.ID, c.ID)
	refused("resume consolidation", err)
	_, err = e.svc.CancelSessionConsolidation(as, alpha.ID, c.ID)
	refused("cancel consolidation", err)
	if c, err = e.svc.CancelSessionConsolidation(owner, alpha.ID, c.ID); err != nil || c.Phase != "cancelled" {
		t.Fatalf("the owner could not cancel: %+v, %v", c, err)
	}
}
