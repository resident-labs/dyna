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

func TestExplicitPostSuppressesOnlySuccessfulCompletion(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failed], func(t *testing.T) {
			e := newTestEnv(t, withImmediateTurns)
			rec := e.room()
			parent := e.start(rec, "lead", nil)
			child := e.start(rec, "child", &parent)
			child.CurrentTurn = 1
			explicit, err := e.svc.Post(e.ctx, PostInput{Sender: Sender{Kind: SenderSession, Room: rec.Name, Session: child}, Mentions: []string{"@parent"}, Text: "The result is recorded."})
			if err != nil {
				t.Fatal(err)
			}
			if failed {
				if _, err := e.mgr.Store.UpdateTurn(child.ID, 1, func(turn *subagent.Turn) { turn.Status, turn.Error = subagent.TurnError, "provider unavailable" }); err != nil {
					t.Fatal(err)
				}
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
			if !failed {
				if result.Message.ID != "" || len(posts) != 1 || posts[0].ID != explicit.Message.ID {
					t.Fatalf("duplicate successful completion: %+v; posts=%+v", result, posts)
				}
			} else if !turnReport(result.Message, room.TurnFailed) || len(posts) != 2 {
				t.Fatalf("failure was suppressed: %+v; posts=%+v", result, posts)
			}
		})
	}
}
