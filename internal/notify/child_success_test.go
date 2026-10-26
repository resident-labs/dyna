package notify

import (
	"reflect"
	"testing"

	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

func TestChildSuccessReportsOnlyToCurrentParent(t *testing.T) {
	e := newTestEnv(t)
	rec := e.room()
	parent := e.start(rec, "lead", nil)
	child := e.start(rec, "child", &parent)
	res, err := e.svc.CompleteSessionTurn(e.ctx, child.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Message.To, []string{parent.Address()}) || res.Message.OwnerReport {
		t.Fatalf("child success must report only to its parent: %+v", res.Message)
	}
	if len(res.Deliveries) != 1 || res.Deliveries[0].Delivery != "session-queued" {
		t.Fatalf("parent delivery = %+v", res.Deliveries)
	}
}

// A question posted to the parent mid-turn is not the report: the turn's final
// result still reaches the parent (D-226).
func TestMidTurnPostToParentDoesNotHideTheFinalReport(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	parent := e.start(rec, "lead", nil)
	child := e.start(rec, "child", &parent)
	child.CurrentTurn = 1
	if _, err := e.svc.Post(e.ctx, PostInput{Sender: Sender{Kind: SenderSession, Room: rec.Name, Session: child}, Mentions: []string{"@parent"}, Text: "Which option sets the lifetime?"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.mgr.Store.UpdateTurn(child.ID, 1, func(turn *subagent.Turn) { turn.Result = "Ready for review." }); err != nil {
		t.Fatal(err)
	}
	result, err := e.svc.CompleteSessionTurn(e.ctx, child.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Message.To, []string{parent.Address()}) || !turnReport(result.Message, room.TurnCompleted) {
		t.Fatalf("the final report was hidden by the earlier post: %+v", result)
	}
}

func TestExplicitPostSuppressesOnlySuccessfulCompletion(t *testing.T) {
	for _, seat := range []bool{false, true} {
		for _, failed := range []bool{false, true} {
			t.Run(map[bool]string{false: "success", true: "failure"}[failed]+"/"+map[bool]string{false: "lead", true: "root seat"}[seat], func(t *testing.T) {
				e := newTestEnv(t, withImmediateTurns)
				rec := e.room()
				var parent subagent.Info
				if seat {
					e.human(rec, "RESPOND: ready", "@root")
					parent = e.wait(e.rootSession(rec).ID, 1)
				} else {
					parent = e.start(rec, "lead", nil)
				}
				child := e.start(rec, "child", &parent)
				child.CurrentTurn = 1
				// With no final text the explicit post is the child's report: no
				// successful completion may repeat it, though a failure still reports.
				if _, err := e.svc.Post(e.ctx, PostInput{Sender: Sender{Kind: SenderSession, Room: rec.Name, Session: child}, Mentions: []string{"@parent"}, Text: "The result is recorded."}); err != nil {
					t.Fatal(err)
				}
				// The turn owes no final text, so the post is its whole report.
				if _, err := e.mgr.Store.UpdateTurn(child.ID, 1, func(turn *subagent.Turn) {
					turn.Result = nil
					if failed {
						turn.Status, turn.Error = subagent.TurnError, "provider unavailable"
					}
				}); err != nil {
					t.Fatal(err)
				}
				result, err := e.svc.CompleteSessionTurn(e.ctx, child.ID, 1)
				if err != nil {
					t.Fatal(err)
				}
				posts, err := e.svc.Rooms.Messages(rec.ID)
				if err != nil {
					t.Fatal(err)
				}
				turn, err := e.mgr.Store.ReadTurn(child.ID, 1)
				if err != nil || !turn.Reported {
					t.Fatalf("completion was not settled: %+v %v", turn, err)
				}
				// Count the child's own turn reports, so the fixture's other posts
				// cannot hide a duplicate.
				reports := 0
				for _, m := range posts {
					if ev, ok := m.TurnResult(); ok && ev.Session == child.Address() {
						reports++
					}
				}
				if !failed {
					if result.Message.ID != "" || reports != 0 {
						t.Fatalf("duplicate successful completion: %+v; reports=%d posts=%+v", result, reports, posts)
					}
				} else if !turnReport(result.Message, room.TurnFailed) || reports != 1 {
					t.Fatalf("failure was suppressed: %+v; reports=%d posts=%+v", result, reports, posts)
				}
			})
		}
	}
}
