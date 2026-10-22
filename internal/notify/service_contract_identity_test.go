package notify

import (
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
	"testing"
	"time"
)

func TestServiceAlertAfterParentChange(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	p := markService(t, e, e.start(rec, "p", nil), "reviewer")
	q := markService(t, e, e.start(rec, "q", nil), "reviewer")
	h := e.start(rec, "h", &p)
	in := PostInput{Sender: Sender{Kind: SenderSession, Room: rec.Name, Session: h}, Mentions: []string{"@parent"}, Kind: "alert", Text: "Dependency failed"}
	first, err := e.svc.Post(e.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(p.ID, 2)
	_, err = e.mgr.Store.UpdateMeta(h.ID, func(m *subagent.Meta) { m.ParentSession = q.ID })
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.svc.Post(e.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if second.Message.ID == first.Message.ID {
		t.Fatalf("new parent received no alert; returned old recipients %v", second.Message.To)
	}
	e.wait(q.ID, 2)
}
func TestServiceHelperDoesNotReusePriorSessionFindings(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	p := markService(t, e, e.start(rec, "p", nil), "reviewer")
	old := e.start(rec, "h", &p)
	_, err := e.svc.Rooms.Post(rec.ID, room.PostInput{From: old.Address(), To: []string{p.Address()}, SessionIDs: map[string]string{p.Address(): p.ID}, Turn: 1, Text: "Old helper findings"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.mgr.Store.UpdateMeta(old.ID, func(m *subagent.Meta) { m.Closed = time.Now() })
	if err != nil {
		t.Fatal(err)
	}
	h := e.start(rec, "h", &p)
	if _, err := e.mgr.Store.UpdateTurn(h.ID, 1, func(turn *subagent.Turn) { turn.Result = nil }); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.CompleteSessionTurn(e.ctx, h.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Message.ID == "" {
		t.Fatal("new helper produced no findings but inherited old helper findings")
	}
	e.wait(p.ID, 2)
}
func TestServiceHelperUnavailableParentAlertsOwner(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	p := markService(t, e, e.start(rec, "p", nil), "reviewer")
	h := e.start(rec, "h", &p)
	_, err := e.mgr.Store.UpdateMeta(p.ID, func(m *subagent.Meta) { m.HarnessSession = "" })
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.CompleteSessionTurn(e.ctx, h.ID, 1)
	if err != nil || len(res.Message.To) != 1 || res.Message.To[0] != rec.Name {
		t.Fatalf("missing owner fallback: recipients %v, error %v", res.Message.To, err)
	}
}
