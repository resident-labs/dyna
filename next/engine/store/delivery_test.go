package store

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestDeliveryReplyRoundTrip(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	s, err := Open(t.TempDir(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRoom(ctx, "room", "room", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.ExecContext(ctx, "INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','room','s','p','ready','',0)"); err != nil {
		t.Fatal(err)
	}
	reply := contract.ReplyChannel{Kind: contract.ReplyFinal, Target: contract.Principal{Kind: contract.PrincipalOwner}, Home: contract.Conversation{Kind: contract.ConversationDM, Session: "s"}, ReplyTo: "post"}
	event := contract.Event{ID: "post", Time: now, Room: "room", From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.PostCreated{Text: "hello"}}
	delivery := contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: "s"}, Surface: contract.SurfaceDM, Reply: reply, Wake: true}
	if _, err := s.Post(ctx, event, []contract.Delivery{delivery}); err != nil {
		t.Fatal(err)
	}
	start := contract.Event{ID: "started", Time: now, Room: "room", From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{}}
	_, inputs, err := s.Dispatch(ctx, "s", "turn", start, false)
	if err != nil || len(inputs) != 1 || inputs[0].Reply != reply || inputs[0].Surface != contract.SurfaceDM {
		t.Fatalf("inputs %+v: %v", inputs, err)
	}
}
