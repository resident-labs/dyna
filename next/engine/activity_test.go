package engine

import (
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestTurnActivity(t *testing.T) {
	s, room, session, ctx := setup(t)
	connectAgent(ctx, t, s, session)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	turn := contract.TurnID("turn_" + session)
	list, e := s.SessionList(ctx, owner, contract.SessionListRequest{Room: room})
	if e != nil {
		t.Fatal(e)
	}
	events := ownerStream(ctx, t, s, room, list.Seq)
	ownerEvents := ownerStream(ctx, t, s, "", list.Seq)
	for _, label := range []string{"\xff", "a\nb", "a\rb", strings.Repeat("x", 81)} {
		if _, e := s.TurnActivity(ctx, caller, contract.TurnActivityRequest{Turn: turn, Label: label}); e == nil || e.Code != contract.ErrInvalid {
			t.Fatalf("label %q: %v", label, e)
		}
	}
	for _, label := range []string{"first", "latest"} {
		if _, e := s.TurnActivity(ctx, caller, contract.TurnActivityRequest{Turn: turn, Label: label}); e != nil {
			t.Fatal(e)
		}
		for _, stream := range []<-chan contract.Event{events, ownerEvents} {
			if event := <-stream; event.Seq != 0 || event.Body.(contract.TurnActivity).Label != label {
				t.Fatalf("activity event: %+v", event)
			}
		}
	}
	list, e = s.SessionList(ctx, owner, contract.SessionListRequest{Room: room})
	if e != nil {
		t.Fatal(e)
	}
	for _, item := range list.Sessions {
		if item.ID == session && item.Activity != "latest" {
			t.Fatalf("activity: %+v", item)
		}
	}
	post, e := s.Post(ctx, owner, contract.PostRequest{Room: room, Text: "after activity"})
	if e != nil {
		t.Fatal(e)
	}
	if event := <-events; event.Seq != post.Seq {
		t.Fatalf("resume point moved: %+v", event)
	}
	if _, e := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: turn, Outcome: contract.TurnCompleted}); e != nil {
		t.Fatal(e)
	}
	list, e = s.SessionList(ctx, owner, contract.SessionListRequest{Room: room})
	if e != nil {
		t.Fatal(e)
	}
	for _, item := range list.Sessions {
		if item.ID == session && item.Activity != "" {
			t.Fatalf("ended activity: %+v", item)
		}
	}
}
