package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestCrossRoomDeliverySurvivesSourceDeletion(t *testing.T) {
	r, ctx := newDeleteRig(t)
	source, root, _ := r.room(t, ctx, "source")
	target, targetRoot, _ := r.room(t, ctx, "target")
	sent, problem := r.s.Post(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: root}, contract.PostRequest{Room: target, Text: "hello", Mentions: []string{"root"}})
	if problem != nil {
		t.Fatal(problem)
	}
	r.delete(t, ctx, source)
	r.settle(t, ctx, source)
	if _, problem := r.s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: targetRoot}, contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	start := contract.Event{ID: "turn-start-test", Time: time.Unix(1000, 0), Room: target, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{}}
	for _, replay := range []bool{false, true} {
		_, inputs, _, err := r.st.Dispatch(ctx, targetRoot, "cross-room-turn", start, replay)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, input := range inputs {
			if int64(input.Seq) != sent.Seq {
				continue
			}
			found = true
			events, err := r.st.Events(ctx, target, sent.Seq-1, 1, []contract.EventType{"post.created"})
			if err != nil || len(events) != 1 {
				t.Fatalf("events: %v %v", events, err)
			}
			frame, _, err := r.s.inputFrame(ctx, targetRoot, events[0], contract.Delivery{Surface: input.Surface, Reply: input.Reply})
			if err != nil || !strings.Contains(frame, "From source:root") || input.Reply.Kind != contract.ReplyPost {
				t.Fatalf("replay %v: %s %v %+v", replay, frame, err, input)
			}
		}
		if !found {
			t.Fatalf("replay %v: missing delivery %+v", replay, inputs)
		}
	}
}
