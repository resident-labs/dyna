package store

import (
	"context"
	"github.com/resident-labs/dyna/contract"
	"testing"
	"time"
)

func TestDiscordDMTakesATurnAlone(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Unix(1000, 0)
	if err := s.CreateRoom(ctx, "room", "room", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.Exec(`INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','room','s','p','open','',0); INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('b','s','','agent','ready','','',0,0)`); err != nil {
		t.Fatal(err)
	}
	input := func(id, channel string, direct bool) {
		t.Helper()
		if err := s.DiscordInput(ctx, contract.Event{ID: contract.EventID(id), Room: "room", Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.DiscordMessage{Channel: channel, Message: id, Confined: direct}}, "s"); err != nil {
			t.Fatal(err)
		}
	}
	notice := func(id string) {
		t.Helper()
		_, err := s.Post(ctx, contract.Event{ID: contract.EventID(id), Room: "room", Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionNotice{Session: "s", Text: id}}, []contract.Delivery{{To: contract.Principal{Kind: contract.PrincipalSession, Session: "s"}, Surface: contract.SurfaceEngine, Wake: true}})
		if err != nil {
			t.Fatal(err)
		}
	}
	dispatch := func(id string, replay bool) []FeedInput {
		t.Helper()
		_, inputs, _, err := s.Dispatch(ctx, "s", contract.TurnID(id), contract.Event{ID: contract.EventID("start-" + id), Room: "room", Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{}}, replay)
		if err != nil {
			t.Fatal(err)
		}
		return inputs
	}
	input("dm", "500", true)
	notice("notice")
	input("server", "200", false)
	inputs := dispatch("dmturn", false)
	if len(inputs) != 1 || inputs[0].Reply.ReplyTo != "dm" {
		t.Fatal(inputs)
	}
	notice("steer")
	if inputs := dispatch("steering", false); len(inputs) != 0 {
		t.Fatal("DM turn accepted steering", inputs)
	}
	if inputs := dispatch("replay", true); len(inputs) != 1 || inputs[0].Reply.ReplyTo != "dm" {
		t.Fatal("replay mixed inputs", inputs)
	}
	end := contract.Event{ID: "end", Room: "room", Time: now, From: contract.Principal{Kind: contract.PrincipalSession, Session: "s"}, Body: contract.TurnEnded{Session: "s", Turn: "dmturn", Outcome: contract.TurnCompleted, Final: "answer"}}
	if err := s.EndTurn(ctx, "s", contract.TurnOutputRequest{Turn: "dmturn", Outcome: contract.TurnCompleted, Final: "answer"}, end); err != nil {
		t.Fatal(err)
	}
	out, ok, err := s.NextDiscordReply(ctx)
	if err != nil || !ok || out.Channel != "500" {
		t.Fatal(out, ok, err)
	}
	now = now.Add(2 * time.Minute)
	inputs = dispatch("next", false)
	if len(inputs) != 3 {
		t.Fatal("other inputs were lost", inputs)
	}
	for _, v := range inputs {
		if v.Reply.ReplyTo == "dm" {
			t.Fatal("DM reused", inputs)
		}
	}
}
