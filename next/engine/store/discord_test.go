package store

import (
	"context"
	"fmt"
	"github.com/resident-labs/dyna/contract"
	"strings"
	"testing"
	"time"
)

func TestDiscordOutboxSurvivesRestartAndKeepsChannelDestinations(t *testing.T) {
	for _, jobs := range []int{0, 1} {
		t.Run(fmt.Sprint(jobs), func(t *testing.T) { discordOutboxRestart(t, jobs) })
	}
}
func discordOutboxRestart(t *testing.T, jobs int) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	final := strings.Repeat("answer", 500)
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
	end := contract.Event{ID: "end", Time: now, Room: "room", From: contract.Principal{Kind: contract.PrincipalSession, Session: "root"}, Body: contract.TurnEnded{Session: "root", Turn: "turn", Outcome: contract.TurnCompleted, Final: final}}
	if err := s.EndTurn(ctx, "root", contract.TurnOutputRequest{Turn: "turn", Outcome: contract.TurnCompleted, Final: final, ShellJobs: jobs}, end); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	owed, err := s.RepliesOwed(ctx, "root")
	if err != nil || len(owed) != 2*jobs {
		t.Fatalf("debts %v %v", owed, err)
	}
	if jobs > 0 {
		if _, err := s.writer.Exec("DELETE FROM discord_outbox"); err != nil {
			t.Fatal(err)
		}
		notice := contract.Event{ID: "notice", Time: now, Room: "room", From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionNotice{Session: "root", Text: "job finished"}}
		if _, err := s.Post(ctx, notice, []contract.Delivery{{To: contract.Principal{Kind: contract.PrincipalSession, Session: "root"}, Surface: contract.SurfaceEngine, Wake: true}}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.writer.Exec("UPDATE boxes SET state='ready',want='run'"); err != nil {
			t.Fatal(err)
		}
		start.ID = "start2"
		start.Time = now.Add(InputBatchWindow + time.Second)
		if active, _, _, err := s.Dispatch(ctx, "root", "turn2", start, false); err != nil || active != "turn2" {
			t.Fatalf("dispatch %s %v", active, err)
		}
		end.ID = "end2"
		end.Time = start.Time.Add(time.Second)
		end.Body = contract.TurnEnded{Session: "root", Turn: "turn2", Outcome: contract.TurnCompleted, Final: final}
		if err := s.EndTurn(ctx, "root", contract.TurnOutputRequest{Turn: "turn2", Outcome: contract.TurnCompleted, Final: final}, end); err != nil {
			t.Fatal(err)
		}
		owed, err := s.RepliesOwed(ctx, "root")
		if err != nil || len(owed) != 0 {
			t.Fatalf("unpaid debts %v %v", owed, err)
		}
	}

	rows, err := s.Read().Query("SELECT channel,message,text,group_key FROM discord_outbox ORDER BY channel,id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	texts := map[string]string{}
	groups := map[string]string{}
	count := 0
	for rows.Next() {
		var channel, message, text, group string
		if err := rows.Scan(&channel, &message, &text, &group); err != nil {
			t.Fatal(err)
		}
		if texts[channel] == "" {
			expected := "two"
			if channel == "200" {
				expected = "three"
			}
			if message != expected || group == "" {
				t.Fatalf("wrong first chunk %s %s %s", channel, message, group)
			}
			groups[channel] = group
		} else if message != "" || groups[channel] != group {
			t.Fatalf("wrong tail %s %s %s", channel, message, group)
		}
		texts[channel] += text
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 4 || texts["200"] != final || texts["201"] != final || groups["200"] == groups["201"] {
		t.Fatalf("wrong chunks: %d %v", count, groups)
	}
}
