package engine

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// Hold a framed turn open across Stop and owner messages. Only the committed
// output notification, not another input or direct Dispatch, may start a turn.
func TestOwnerAfterStopWakesNextFramedTurn(t *testing.T) {
	for _, tc := range []struct {
		name                                  string
		stop, before, after, repeat, consumed bool
		outcome                               contract.TurnOutcome
		cause                                 contract.FailureCause
		exhausted                             bool
		want                                  bool
	}{
		{name: "owner-after-stop", stop: true, after: true, outcome: contract.TurnStopped, want: true},
		{name: "owner-before-stop", stop: true, before: true, outcome: contract.TurnStopped, want: true},
		{name: "stop-without-input", stop: true, outcome: contract.TurnStopped},
		{name: "repeat-stop-preserves-owner", stop: true, after: true, repeat: true, outcome: contract.TurnStopped, want: true},
		{name: "repeat-stop-then-new-owner", stop: true, before: true, repeat: true, after: true, outcome: contract.TurnStopped, want: true},
		{name: "stopped-without-request", after: true, outcome: contract.TurnStopped, want: true},
		{name: "completed", after: true, outcome: contract.TurnCompleted, want: true},
		{name: "consumed-steer", after: true, consumed: true, outcome: contract.TurnStopped},
		{name: "terminal-failure", after: true, outcome: contract.TurnFailed, cause: contract.CauseSetModelFailed, want: true},
		{name: "exhausted-model-failure", after: true, outcome: contract.TurnFailed, cause: contract.CauseModelFailed, exhausted: true, want: true},
		{name: "scheduled-model-failure", outcome: contract.TurnFailed, cause: contract.CauseModelFailed},
		{name: "new-owner-overrides-model-retry", after: true, outcome: contract.TurnFailed, cause: contract.CauseModelFailed, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, room, worker, ctx := setup(t)
			caller := boxCaller(worker)
			channel, problem := testChannel(t, s, ctx, caller)
			if problem != nil {
				t.Fatal(problem)
			}
			start := (<-channel).Body.(contract.TurnStart)
			stop := func(expectFrame bool) {
				t.Helper()
				if _, problem := s.SessionStop(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStopRequest{Session: worker}); problem != nil {
					t.Fatal(problem)
				}
				if expectFrame {
					select {
					case msg := <-channel:
						if _, ok := msg.Body.(contract.TurnStop); !ok {
							t.Fatalf("want stop, got %T", msg.Body)
						}
					case <-time.After(3 * time.Second):
						t.Fatal("stop not dispatched")
					}
				}
			}
			var seq contract.DeliverySeq
			post := func() {
				t.Helper()
				if _, problem := s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: worker}, Text: "owner input"}); problem != nil {
					t.Fatal(problem)
				}
				select {
				case msg := <-channel:
					steer, ok := msg.Body.(contract.TurnSteer)
					if !ok || steer.Queue != contract.QueueSteer || steer.Interrupt == nil || steer.Interrupt.Kind != contract.InterruptOwner {
						t.Fatalf("want owner steer, got %+v", msg.Body)
					}
					seq = steer.Input.Delivery
				case <-time.After(3 * time.Second):
					t.Fatal("owner input not dispatched during turn")
				}
			}
			if tc.before {
				post()
			}
			if tc.stop {
				stop(true)
			}
			if tc.repeat && tc.before {
				stop(false)
			}
			if tc.after {
				post()
			}
			if tc.repeat && !tc.before {
				stop(false)
			}
			if tc.consumed {
				if _, problem := s.DeliveryDone(ctx, caller, contract.DeliveryDoneRequest{Delivery: seq}); problem != nil {
					t.Fatal(problem)
				}
			}
			if tc.exhausted {
				if err := s.store.Write(ctx, func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, "UPDATE sessions SET model_retries=8 WHERE id=?", worker)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			output := contract.TurnOutputRequest{Turn: start.Turn, Outcome: tc.outcome, Cause: tc.cause}
			if _, problem := s.TurnOutput(ctx, caller, output); problem != nil {
				t.Fatal(problem)
			}
			// Replayed output must not create another wake or another active turn.
			if _, problem := s.TurnOutput(ctx, caller, output); problem != nil {
				t.Fatal(problem)
			}
			wait := 200 * time.Millisecond
			if tc.want {
				wait = 3 * time.Second
			}
			select {
			case msg := <-channel:
				next, ok := msg.Body.(contract.TurnStart)
				if !tc.want || !ok || next.Turn == start.Turn || len(next.Inputs) == 0 || next.Inputs[len(next.Inputs)-1].Delivery != seq || !strings.Contains(next.Inputs[len(next.Inputs)-1].Text, "owner input") {
					t.Fatalf("wrong next turn: %+v", msg.Body)
				}
				if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: next.Turn, Outcome: contract.TurnCompleted}); problem != nil {
					t.Fatal(problem)
				}
				select {
				case msg := <-channel:
					t.Fatalf("duplicate/replayed input: %+v", msg.Body)
				case <-time.After(40 * time.Millisecond):
				}
			case <-time.After(wait):
				if tc.want {
					t.Fatal("owner input stranded after stopped turn; no next TurnStart")
				}
			}
		})
	}
}
