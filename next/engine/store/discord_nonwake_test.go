package store

import (
	"context"
	"github.com/resident-labs/dyna/contract"
	"testing"
	"time"
)

func TestReviewDiscordNonWakeHead(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	if err := s.CreateRoom(ctx, "room", "room", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.Exec(`INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','room','s','p','open','',0); INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('b','s','','agent','ready','','',0,0)`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"old", "new"} {
		if err := s.DiscordInput(ctx, contract.Event{ID: contract.EventID(id), Room: "room", Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.DiscordMessage{Channel: "200", Message: id, Text: "hi"}}, "s"); err != nil {
			t.Fatal(err)
		}
		if id == "old" {
			if _, err := s.writer.Exec("UPDATE deliveries SET wake=0 WHERE to_id='s'"); err != nil {
				t.Fatal(err)
			}
		}
	}
	turn, inputs, _, err := s.Dispatch(ctx, "s", "turn", contract.Event{ID: "start", Room: "room", Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if turn != "turn" || len(inputs) != 2 || inputs[0].Reply.ReplyTo != "old" || inputs[1].Reply.ReplyTo != "new" {
		t.Fatalf("new Discord wake stranded behind non-wake Discord: turn %s, inputs %+v", turn, inputs)
	}
}
