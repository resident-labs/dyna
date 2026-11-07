package engine

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestStopNonOwnerAndMixedPendingInputs(t *testing.T) {
	for _, mode := range []string{"non-owner", "nonwake", "mixed-owner"} {
		t.Run(mode, func(t *testing.T) {
			s, room, worker, ctx := setup(t)
			caller := boxCaller(worker)
			channel, problem := testChannel(t, s, ctx, caller)
			if problem != nil {
				t.Fatal(problem)
			}
			start := (<-channel).Body.(contract.TurnStart)
			event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.PostCreated{Text: "non-owner input"})
			if err != nil {
				t.Fatal(err)
			}
			seq, err := s.store.Post(ctx, event, []contract.Delivery{{To: contract.Principal{Kind: contract.PrincipalSession, Session: worker}, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: mode != "nonwake"}})
			if err != nil {
				t.Fatal(err)
			}
			if mode != "nonwake" {
				select {
				case msg := <-channel:
					if _, ok := msg.Body.(contract.TurnSteer); !ok {
						t.Fatalf("want steer, got %+v", msg.Body)
					}
				case <-time.After(time.Second):
					t.Fatal("no non-owner steer")
				}
			}
			if mode == "mixed-owner" {
				if _, problem := s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: worker}, Text: "owner input"}); problem != nil {
					t.Fatal(problem)
				}
				<-channel // owner steer is taken, but no model consumed it
			}
			if _, problem := s.SessionStop(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStopRequest{Session: worker}); problem != nil {
				t.Fatal(problem)
			}
			<-channel // stop is observed before completing the framed turn
			if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnStopped}); problem != nil {
				t.Fatal(problem)
			}
			var wake bool
			if err := s.store.Read().QueryRowContext(ctx, "SELECT wake FROM deliveries WHERE to_id=? AND seq=?", worker, seq).Scan(&wake); err != nil {
				t.Fatal(err)
			}
			if wake {
				t.Fatal("Stop preserved non-owner wake")
			}
			select {
			case msg := <-channel:
				next, ok := msg.Body.(contract.TurnStart)
				if mode != "mixed-owner" || !ok || len(next.Inputs) != 2 {
					t.Fatalf("unexpected next turn: %+v", msg.Body)
				}
			case <-time.After(200 * time.Millisecond):
				if mode == "mixed-owner" {
					t.Fatal("mixed queue lost owner wake")
				}
			}
		})
	}
}

// EndTurn may commit with the dispatcher disconnected, before a pending input
// has ever been assigned as a steer. Reconnect must start it exactly once.
func TestPendingOwnerAtStoppedCompletionReconnect(t *testing.T) {
	s, room, worker, ctx := setup(t)
	caller := boxCaller(worker)
	turnCtx, cancel := context.WithCancel(ctx)
	channel, problem := testChannel(t, s, turnCtx, caller)
	if problem != nil {
		t.Fatal(problem)
	}
	start := (<-channel).Body.(contract.TurnStart)
	cancel()
	for range channel {
	}
	if _, problem := s.SessionStop(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStopRequest{Session: worker}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: worker}, Text: "pending during stop"}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnStopped}); problem != nil {
		t.Fatal(problem)
	}
	next, problem := testChannel(t, s, ctx, caller)
	if problem != nil {
		t.Fatal(problem)
	}
	select {
	case msg := <-next:
		turn, ok := msg.Body.(contract.TurnStart)
		if !ok || turn.Turn == start.Turn || len(turn.Inputs) != 1 {
			t.Fatalf("wrong reconnect: %+v", msg.Body)
		}
	case <-time.After(time.Second):
		t.Fatal("pending owner lost across completion/reconnect")
	}
}
