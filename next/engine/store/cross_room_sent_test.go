package store

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestCrossRoomSentDoesNotReplaceOwnerReply(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, room := range []contract.RoomID{"source", "target"} {
		if err := s.CreateRoom(ctx, room, string(room), now); err != nil {
			t.Fatal(err)
		}
	}
	_, err = s.writer.ExecContext(ctx, `INSERT INTO sessions(id,room,name,profile,state,goal,created_at,parent) VALUES('src','source','root','p','open','',0,NULL),('dst','target','root','p','open','',0,NULL); INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('bs','src','','agent','ready','','',0,0),('bt','dst','','agent','ready','','',0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	src := contract.Principal{Kind: contract.PrincipalSession, Session: "src"}
	dst := contract.Principal{Kind: contract.PrincipalSession, Session: "dst"}
	feed := contract.Conversation{Kind: contract.ConversationFeed}
	input := contract.Event{ID: "input", Time: now, Room: "source", From: owner, Body: contract.PostCreated{Text: "work", Home: feed}}
	if _, err := s.Post(ctx, input, []contract.Delivery{{To: src, Surface: contract.SurfaceFeed, Wake: true, Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: owner, Home: feed}}}); err != nil {
		t.Fatal(err)
	}
	start := contract.Event{ID: "start", Time: now, Room: "source", From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{}}
	if _, _, _, err := s.Dispatch(ctx, "src", "turn", start, false); err != nil {
		t.Fatal(err)
	}
	post := contract.Event{ID: "cross", Time: now, Room: "target", From: src, Body: contract.PostCreated{Text: "hello", Home: feed, Mentions: []contract.Principal{dst}}}
	seq, err := s.Post(ctx, post, []contract.Delivery{{To: dst, Surface: contract.SurfaceFeed, Wake: true, Reply: contract.ReplyChannel{Kind: contract.ReplyPost}}})
	if err != nil {
		t.Fatal(err)
	}
	sent, err := s.Events(ctx, "source", seq-1, 10, []contract.EventType{"post.sent"})
	if err != nil || len(sent) != 1 {
		t.Fatalf("sent: %+v %v", sent, err)
	}
	var count, wakes int
	if err := s.Read().QueryRowContext(ctx, "SELECT count(*), coalesce(sum(wake),0) FROM deliveries WHERE seq=?", sent[0].Seq).Scan(&count, &wakes); err != nil || count != 0 || wakes != 0 {
		t.Fatalf("sender deliveries: %d wakes: %d error: %v", count, wakes, err)
	}
	if err := s.Read().QueryRowContext(ctx, "SELECT count(*), coalesce(sum(CASE WHEN wake AND to_id='dst' THEN 1 ELSE 0 END),0) FROM deliveries WHERE seq=?", seq).Scan(&count, &wakes); err != nil || count != 1 || wakes != 1 {
		t.Fatalf("target deliveries: %d wakes: %d error: %v", count, wakes, err)
	}
	end := contract.Event{ID: "end", Time: now, Room: "source", From: src, Body: contract.TurnEnded{Session: "src", Turn: "turn", Outcome: contract.TurnCompleted, Final: "owner reply"}}
	if err := s.EndTurn(ctx, "src", contract.TurnOutputRequest{Turn: "turn", Outcome: contract.TurnCompleted, Final: "owner reply"}, end); err != nil {
		t.Fatal(err)
	}
	events, err := s.Events(ctx, "source", 0, 100, []contract.EventType{"turn.ended"})
	if err != nil || len(events) != 1 {
		t.Fatalf("end: %+v %v", events, err)
	}
	body := events[0].Body.(contract.TurnEnded)
	if body.Final != "owner reply" || len(body.Replies) != 1 || body.Replies[0].Posted != "" {
		t.Fatalf("owner reply: %+v", body)
	}
}
