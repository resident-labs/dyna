package engine

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord"
)

func TestDiscordExplicitReplyPaysInput(t *testing.T) {
	for _, tc := range []struct {
		name, channel, reply string
		waiting, late, api   bool
		want                 int
	}{
		{name: "matching", channel: "200", reply: "300", want: 1},
		{name: "matching through api", channel: "200", reply: "300", api: true, want: 1},
		{name: "unthreaded through api", channel: "200", api: true, want: 2},
		{name: "unthreaded", channel: "200", want: 2},
		{name: "other channel", channel: "201", reply: "300", want: 2},
		{name: "no send", want: 1},
		{name: "waiting paid early", channel: "200", reply: "300", waiting: true, want: 1},
		{name: "waiting paid later", channel: "200", reply: "300", waiting: true, late: true, want: 2},
		// The waiting turn's text was the reply; the turn after the wait must not answer Discord again.
		{name: "waiting no send", waiting: true, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r, root, ctx, fake, client := discordFixture(t)
			if err := s.store.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, "UPDATE sessions SET closed_at=1 WHERE parent=?", root)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			s.discordDirectory.Apply("CHANNEL_CREATE", json.RawMessage(`{"id":"201","guild_id":"100","name":"other","type":0}`))
			var m discord.Message
			if err := json.Unmarshal([]byte(`{"id":"300","channel_id":"200","guild_id":"100","content":"question","author":{"id":"2","username":"ari"}}`), &m); err != nil {
				t.Fatal(err)
			}
			if err := s.discordInput(ctx, client, m); err != nil {
				t.Fatal(err)
			}
			if err := s.store.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, "UPDATE boxes SET state='ready',want='run' WHERE session=?", root)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			start := func(turn contract.TurnID) {
				t.Helper()
				e, err := s.event(r, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
				if err != nil {
					t.Fatal(err)
				}
				e.Time = e.Time.Add(time.Hour)
				if active, _, _, err := s.store.Dispatch(ctx, root, turn, e, false); err != nil || active != turn {
					t.Fatalf("dispatch %s: %s %v", turn, active, err)
				}
			}
			end := func(turn contract.TurnID, jobs int) {
				t.Helper()
				out := contract.TurnOutputRequest{Turn: turn, Outcome: contract.TurnCompleted, Final: "final answer", ShellJobs: jobs}
				e, err := s.event(r, boxCaller(root), contract.TurnEnded{Session: root, Turn: turn, Outcome: out.Outcome, Final: out.Final})
				if err != nil {
					t.Fatal(err)
				}
				if err := s.store.EndTurn(ctx, root, out, e); err != nil {
					t.Fatal(err)
				}
			}
			send := func() {
				t.Helper()
				if tc.api {
					body := `{"content":"explicit answer"}`
					if tc.reply != "" {
						body = `{"content":"explicit answer","message_reference":{"message_id":"` + tc.reply + `"}}`
					}
					if _, problem := s.DiscordAPI(ctx, boxCaller(root), contract.DiscordAPIRequest{Method: "POST", Path: "/channels/" + tc.channel + "/messages", Body: json.RawMessage(body)}); problem != nil {
						t.Fatal(problem)
					}
				} else if tc.channel != "" {
					if _, problem := s.DiscordSend(ctx, boxCaller(root), contract.DiscordSendRequest{Channel: tc.channel, ReplyTo: tc.reply, Text: "explicit answer"}); problem != nil {
						t.Fatal(problem)
					}
				}
			}
			start("first")
			if !tc.late {
				send()
			}
			jobs := 0
			if tc.waiting {
				jobs = 1
			}
			end("first", jobs)
			if tc.waiting {
				e, err := s.event(r, contract.Principal{Kind: contract.PrincipalEngine}, contract.SessionNotice{Session: root, Text: "job finished"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.store.Post(ctx, e, []contract.Delivery{{To: boxCaller(root), Surface: contract.SurfaceEngine, Wake: true}}); err != nil {
					t.Fatal(err)
				}
				if err := s.store.Write(ctx, func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, "UPDATE boxes SET state='ready',want='run' WHERE session=?", root)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				start("second")
				if tc.late {
					send()
				}
				end("second", 0)
			}
			// Drain the durable final-reply outbox through the same fake Discord client.
			for {
				reply, found, err := s.store.NextDiscordReply(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if !found {
					break
				}
				if _, _, err := client.SendMessage(ctx, reply.Channel, reply.Text, reply.Message); err != nil {
					t.Fatal(err)
				}
				if err := s.store.DiscordReplySent(ctx, reply.ID, nil); err != nil {
					t.Fatal(err)
				}
			}
			if got := len(fake.Messages); got != tc.want {
				t.Fatalf("messages = %d, want %d", got, tc.want)
			}
			var debts int
			if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM replies_owed WHERE session=?", root).Scan(&debts); err != nil {
				t.Fatal(err)
			}
			if debts != 0 {
				t.Fatal("reply debt remains")
			}
			// A late send pays nothing: the waiting turn's text already answered, and the outbox drain clears typing.
			if tc.reply == "300" && tc.channel == "200" && !tc.late {
				s.discordRuntime.Lock()
				_, typing := s.discordTyping["200"]
				s.discordRuntime.Unlock()
				if typing {
					t.Fatal("typing remains after explicit reply")
				}
			}
		})
	}
}

func TestDiscordAPIPathNormalization(t *testing.T) {
	s, _, root, ctx, _, _ := discordFixture(t)
	for _, tc := range []struct {
		path string
		code contract.ErrorCode
	}{
		{"/channels/200/messages?around=300&limit=8", ""},
		{"/api/v10/channels/200/messages?around=300&limit=8", ""},
		{"/api/v9/channels/200/messages", contract.ErrInvalid},
		{"/channels/999/messages", contract.ErrForbidden},
		{"https://discord.com/api/v10/channels/200/messages", contract.ErrInvalid},
		{"/channels/200/../messages", contract.ErrInvalid},
	} {
		t.Run(tc.path, func(t *testing.T) {
			_, problem := s.DiscordAPI(ctx, boxCaller(root), contract.DiscordAPIRequest{Method: "GET", Path: tc.path})
			if tc.code == "" {
				if problem != nil {
					t.Fatal(problem)
				}
			} else if problem == nil || problem.Code != tc.code {
				t.Fatalf("problem %v, want %s", problem, tc.code)
			}
		})
	}
}
