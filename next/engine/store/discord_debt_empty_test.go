package store

import (
	"context"
	"encoding/json"
	"github.com/resident-labs/dyna/contract"
	"strings"
	"testing"
	"time"
)

func TestDiscordWaitingEmptyEndPostKeepsEarlierDebt(t *testing.T) {
	jobs := 1
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	final := strings.Repeat("answer", 0)
	now := time.Unix(1000, 0)
	if err := s.CreateRoom(ctx, "room", "room", now); err != nil {
		t.Fatal(err)
	}
	_, err = s.writer.Exec(`INSERT INTO sessions(id,room,name,profile,state,goal,created_at,parent) VALUES('root','room','root','p','open','',0,NULL); INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('b','root','','agent','ready','','',0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	for i, channel := range []string{"200", "201", "200"} {
		id := contract.EventID([]string{"one", "two", "three"}[i])
		b := contract.DiscordMessage{Channel: channel, Message: string(id), Text: "hi"}
		event := contract.Event{ID: id, Time: now, Room: "room", From: contract.Principal{Kind: contract.PrincipalEngine}, Body: b}
		d := contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: "root"}, Surface: contract.SurfaceDiscord, Wake: true, Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: contract.Principal{Kind: contract.PrincipalEngine}, Home: contract.Conversation{Kind: contract.ConversationDiscord, Channel: channel}, ReplyTo: id}}
		if _, err := s.Post(ctx, event, []contract.Delivery{d}); err != nil {
			t.Fatal(err)
		}
	}
	start := contract.Event{ID: "start", Time: now, Room: "room", From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{}}
	if _, _, _, err := s.Dispatch(ctx, "root", "turn", start, false); err != nil {
		t.Fatal(err)
	}
	// Seed debts directly: this test covers cross-turn destination consolidation, not dispatch.
	if _, err := s.writer.Exec("UPDATE deliveries SET state='taken',turn='turn' WHERE to_id='root'"); err != nil {
		t.Fatal(err)
	}
	debts := []contract.ReplyChannel{{Kind: contract.ReplyFinal, Target: contract.Principal{Kind: contract.PrincipalEngine}, Home: contract.Conversation{Kind: contract.ConversationDiscord, Channel: "200"}, ReplyTo: "one"}}
	encoded, _ := json.Marshal(debts)
	if _, err := s.writer.Exec("INSERT INTO replies_owed(session,replies) VALUES('root',?)", encoded); err != nil {
		t.Fatal(err)
	}
	first := ""
	post := contract.Event{ID: "answerpost", Time: now, Room: "room", From: contract.Principal{Kind: contract.PrincipalSession, Session: "root"}, Body: contract.PostCreated{Text: final}}
	if _, err := s.Post(ctx, post, nil); err != nil {
		t.Fatal(err)
	}

	end := contract.Event{ID: "end", Time: now, Room: "room", From: contract.Principal{Kind: contract.PrincipalSession, Session: "root"}, Body: contract.TurnEnded{Session: "root", Turn: "turn", Outcome: contract.TurnCompleted, Final: first}}
	if err := s.EndTurn(ctx, "root", contract.TurnOutputRequest{Turn: "turn", Outcome: contract.TurnCompleted, Final: first, ShellJobs: jobs, EndPosts: []contract.EventID{"answerpost"}}, end); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.Read().QueryRow("SELECT COUNT(*) FROM discord_outbox").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("unexpected reply")
	}
	t.Logf("outbox chunks sent: %d", count)
	owed, err := s.RepliesOwed(ctx, "root")
	if err != nil || len(owed) != 1 {
		t.Fatalf("answered channel still owed: %v %v", owed, err)
	}
}
