package engine

import (
	"database/sql"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord"
	"testing"
	"time"
)

func TestDiscordDMOnlyTurn(t *testing.T) {
	f := discordFixture(t)
	f.fake.SetMemberRoles(nil)
	m := discord.Message{ID: "400", Channel: "500"}
	m.Author.ID = "2"
	if err := f.s.discordInput(f.ctx, f.client, m); err != nil {
		t.Fatal(err)
	}
	events, err := f.s.store.Events(f.ctx, f.room, 0, 100, []contract.EventType{"discord.message"})
	if err != nil || len(events) != 0 {
		t.Fatal("roleless DM delivered", events, err)
	}
	f.fake.SetMemberRoles([]string{"10"})
	m.ID = "401"
	if err := f.s.discordInput(f.ctx, f.client, m); err != nil {
		t.Fatal(err)
	}
	events, err = f.s.store.Events(f.ctx, f.room, 0, 100, []contract.EventType{"discord.message"})
	if err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
	b := events[0].Body.(contract.DiscordMessage)
	if !b.Direct || b.ChannelName != "direct message" || len(b.Roles) != 1 {
		t.Fatal(b)
	}
	if err := f.s.store.Write(f.ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(f.ctx, "INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,started_seq) VALUES('dmturn',?,0,'','',0,0,0,0,0)", f.root); err != nil {
			return err
		}
		_, err := tx.ExecContext(f.ctx, "UPDATE deliveries SET turn='dmturn',state='taken' WHERE to_id=?", f.root)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	caller := boxCaller(f.root)
	if _, p := f.s.DiscordSend(f.ctx, caller, contract.DiscordSendRequest{Channel: "200", Text: "injected"}); p == nil || p.Message != confinedDiscordRefusal().Message {
		t.Fatal(p)
	}
	if _, p := f.s.DiscordAPI(f.ctx, caller, contract.DiscordAPIRequest{Method: "POST", Path: "/channels/500/messages", Body: []byte(`{"content":"injected"}`)}); p != nil {
		t.Fatal(p)
	}
	if _, p := f.s.DiscordSend(f.ctx, caller, contract.DiscordSendRequest{Channel: "500", Text: "answer"}); p != nil {
		t.Fatal(p)
	}

	end := contract.Event{ID: "dmend", Time: time.Now(), Room: f.room, From: caller, Body: contract.TurnEnded{Session: f.root, Turn: "dmturn", Outcome: contract.TurnCompleted, Final: "final"}}
	if err := f.s.store.EndTurn(f.ctx, f.root, contract.TurnOutputRequest{Turn: "dmturn", Outcome: contract.TurnCompleted, Final: "final"}, end); err != nil {
		t.Fatal(err)
	}
	out, ok, err := f.s.store.NextDiscordReply(f.ctx)
	if err != nil || !ok || out.Channel != "500" {
		t.Fatal(out, ok, err)
	}

}
