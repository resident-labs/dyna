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
	_, err = s.writer.ExecContext(ctx, "INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('parent','room','parent','p','open','',0),('child','room','child','p','open','',0); INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('b','child','','agent','ready','','',0,0)")
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
	if _, _, _, err := s.Dispatch(ctx, "child", "turn", start, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TurnSections(ctx, "turn", []contract.ContextSection{{Key: contract.ContextProfiles, Text: "snapshot"}}); err != nil {
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
	if _, saved, err := s.SavedTurnSections(ctx, "turn"); err != nil || saved {
		t.Fatalf("ended turn retains snapshot: saved=%v err=%v", saved, err)
	}
	if _, err := s.TurnSections(ctx, "turn", []contract.ContextSection{{Key: contract.ContextProfiles, Text: "late"}}); err == nil {
		t.Fatal("saved a snapshot after the turn ended")
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

// A post to the target from the same millisecond but before the turn started is not this
// turn's reply: it must not be picked, and it must not make the final reply quiet.
func TestEndTurnIgnoresPostBeforeTurnStart(t *testing.T) {
	for _, later := range []bool{false, true} {
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
		if _, err = s.writer.ExecContext(ctx, "INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('parent','room','parent','p','open','',0),('child','room','child','p','open','',0); INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('b','child','','agent','ready','','',0,0)"); err != nil {
			t.Fatal(err)
		}
		parent := contract.Principal{Kind: contract.PrincipalSession, Session: "parent"}
		child := contract.Principal{Kind: contract.PrincipalSession, Session: "child"}
		feed := contract.Conversation{Kind: contract.ConversationFeed}
		post := func(id contract.EventID, from contract.Principal, deliveries []contract.Delivery) {
			event := contract.Event{ID: id, Time: now, Room: "room", From: from, Body: contract.PostCreated{Text: string(id), Home: feed, Mentions: []contract.Principal{parent}}}
			if _, err := s.Post(ctx, event, deliveries); err != nil {
				t.Fatal(err)
			}
		}
		post("brief", parent, []contract.Delivery{{To: child, Surface: contract.SurfaceFeed, Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: parent, Home: feed}, Wake: true}})
		post("early", child, nil)
		start := contract.Event{ID: "start", Time: now, Room: "room", From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{}}
		if _, _, _, err := s.Dispatch(ctx, "child", "turn", start, false); err != nil {
			t.Fatal(err)
		}
		want := contract.EventID("")
		if later {
			want = "later"
			post(want, child, nil)
		}
		end := contract.Event{ID: "end", Time: now, Room: "room", From: child, Body: contract.TurnEnded{Session: "child", Turn: "turn", Outcome: contract.TurnCompleted, Final: "result"}}
		if err := s.EndTurn(ctx, "child", contract.TurnOutputRequest{Turn: "turn", Outcome: contract.TurnCompleted, Final: "result"}, end); err != nil {
			t.Fatal(err)
		}
		events, err := s.Events(ctx, "room", 0, 100, []contract.EventType{"turn.ended"})
		if err != nil || len(events) != 1 {
			t.Fatalf("events %+v %v", events, err)
		}
		replies := events[0].Body.(contract.TurnEnded).Replies
		var wake bool
		if err := s.Read().QueryRowContext(ctx, "SELECT wake FROM deliveries WHERE seq=? AND to_id='parent'", events[0].Seq).Scan(&wake); err != nil {
			t.Fatal(err)
		}
		if len(replies) != 1 || replies[0].Posted != want || wake == later {
			t.Fatalf("later post %t: replies %+v, wake %t", later, replies, wake)
		}
	}
}
