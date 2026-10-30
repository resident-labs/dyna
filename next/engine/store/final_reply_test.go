package store

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestEndTurnFinalReply(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRoom(ctx, "room", "room", now); err != nil {
		t.Fatal(err)
	}
	_, err = s.writer.ExecContext(ctx, "INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('parent','room','parent','p','ready','',0),('child','room','child','p','ready','',0)")
	if err != nil {
		t.Fatal(err)
	}
	parent := contract.Principal{Kind: contract.PrincipalSession, Session: "parent"}
	child := contract.Principal{Kind: contract.PrincipalSession, Session: "child"}
	feed := contract.Conversation{Kind: contract.ConversationFeed}
	event := contract.Event{ID: "brief", Time: now, Room: "room", From: parent, Body: contract.PostCreated{Text: "brief", Home: feed}}
	d := contract.Delivery{To: child, Surface: contract.SurfaceFeed, Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: parent, Home: feed}, Wake: true}
	if _, err := s.Post(ctx, event, []contract.Delivery{d}); err != nil {
		t.Fatal(err)
	}
	start := contract.Event{ID: "start", Time: now, Room: "room", From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{}}
	if _, _, err := s.Dispatch(ctx, "child", "turn", start, false); err != nil {
		t.Fatal(err)
	}
	post := contract.Event{ID: "reply", Time: now, Room: "room", From: child, Body: contract.PostCreated{Text: "done", Home: feed, Mentions: []contract.Principal{parent}}}
	if _, err := s.Post(ctx, post, nil); err != nil {
		t.Fatal(err)
	}
	output := contract.TurnOutputRequest{Turn: "turn", Outcome: contract.TurnCompleted, Final: "result"}
	end := contract.Event{ID: "end", Time: now, Room: "room", From: child, Body: contract.TurnEnded{Session: "child", Turn: "turn", Outcome: contract.TurnCompleted, Final: "result"}}
	if err := s.EndTurn(ctx, "child", output, end); err != nil {
		t.Fatal(err)
	}
	events, err := s.Events(ctx, "room", 0, 100, []contract.EventType{"turn.ended"})
	if err != nil || len(events) != 1 {
		t.Fatalf("events %+v %v", events, err)
	}
	body := events[0].Body.(contract.TurnEnded)
	if len(body.Replies) != 1 || body.Replies[0].Posted != "reply" {
		t.Fatalf("replies %+v", body.Replies)
	}
	var wake bool
	if err := s.Read().QueryRowContext(ctx, "SELECT wake FROM deliveries WHERE seq=? AND to_id='parent'", events[0].Seq).Scan(&wake); err != nil || wake {
		t.Fatalf("wake %v: %v", wake, err)
	}
}
