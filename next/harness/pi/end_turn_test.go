package pi

import (
	"reflect"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

const (
	assistantText = `{"type":"message_end","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"answer"}]}}`
	narratedPost  = `{"type":"message_end","message":{"role":"assistant","stopReason":"toolUse","content":[{"type":"text","text":"Sending it."},{"type":"toolCall","id":"a"}]}}`
	twoCalls      = `{"type":"message_end","message":{"role":"assistant","stopReason":"toolUse","content":[{"type":"toolCall","id":"a"},{"type":"toolCall","id":"b"}]}}`
	endResult     = `{"type":"message_end","message":{"role":"toolResult","toolCallId":"a","details":{"endPost":"post_a"}}}`
	endResult2    = `{"type":"message_end","message":{"role":"toolResult","toolCallId":"b","details":{"endPost":"post_b"}}}`
	plainResult   = `{"type":"message_end","message":{"role":"toolResult","toolCallId":"b","details":{}}}`
	failedResult  = `{"type":"message_end","message":{"role":"toolResult","toolCallId":"a","isError":true,"details":{"endPost":"post_a"}}}`
	steered       = `{"type":"message_end","message":{"role":"user","deliveryId":"22","content":"more"}}`
	settledEvent  = `{"type":"agent_settled","aborted":false}`
)

func lines(entries ...string) string { return strings.Join(entries, "\n") + "\n" }

func TestEndPostEndsTheTurnWithoutFinalText(t *testing.T) {
	got := replayTurn(t, lines(narratedPost, endResult, settledEvent))
	if got.Outcome != contract.TurnCompleted || got.Final != "" || !reflect.DeepEqual(got.EndPosts, []contract.EventID{"post_a"}) {
		t.Fatalf("result %+v", got)
	}
}

func TestEveryPostOfAnEndBatchIsReported(t *testing.T) {
	got := replayTurn(t, lines(twoCalls, endResult, endResult2, settledEvent))
	if !reflect.DeepEqual(got.EndPosts, []contract.EventID{"post_a", "post_b"}) {
		t.Fatalf("result %+v", got)
	}
}

// Pi keeps going after these batches, so the turn ends on text as usual.
func TestTurnsThatContinuePastAnEndPost(t *testing.T) {
	for name, stream := range map[string]string{
		"batch with another tool": lines(twoCalls, endResult, plainResult, assistantText, settledEvent),
		"failed post":             lines(narratedPost, failedResult, assistantText, settledEvent),
		"steering":                lines(narratedPost, endResult, steered, assistantText, settledEvent),
	} {
		t.Run(name, func(t *testing.T) {
			got := replayTurn(t, stream)
			if got.Final != "answer" || got.EndPosts != nil {
				t.Fatalf("result %+v", got)
			}
		})
	}
}

func TestStoppedTurnReportsNoEndPost(t *testing.T) {
	got := replayTurn(t, lines(narratedPost, endResult, `{"type":"agent_settled","aborted":true}`))
	if got.Outcome != contract.TurnStopped || got.EndPosts != nil {
		t.Fatalf("result %+v", got)
	}
}

func recorded(line string) string {
	return strings.Replace(line, `{"type":"message_end",`, `{"type":"message",`, 1)
}

func TestRecoveryOfTurnEndedByPost(t *testing.T) {
	h := recoveryFixture(t, strings.Join([]string{recorded(narratedPost), recorded(endResult)}, "\n"))
	result, done, err := h.Recover("turn_a", []contract.TurnInput{{Delivery: 9}})
	if err != nil || !done || result.Outcome != contract.TurnCompleted || result.Final != "" || !reflect.DeepEqual(result.EndPosts, []contract.EventID{"post_a"}) {
		t.Fatalf("result=%+v done=%v err=%v", result, done, err)
	}
}

func TestRecoveryContinuesPastAnUnfinishedEndBatch(t *testing.T) {
	for name, tail := range map[string][]string{
		"sibling tool": {recorded(twoCalls), recorded(endResult), recorded(plainResult)},
		"failed post":  {recorded(narratedPost), recorded(failedResult)},
		"steering":     {recorded(narratedPost), recorded(endResult), recorded(steered)},
	} {
		t.Run(name, func(t *testing.T) {
			_, done, err := recoveryFixture(t, strings.Join(tail, "\n")).Recover("turn_a", []contract.TurnInput{{Delivery: 9}})
			if err != nil || done {
				t.Fatalf("done=%v err=%v", done, err)
			}
		})
	}
}
