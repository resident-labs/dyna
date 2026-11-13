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
			fixture := discordFixture(t)
			if err := fixture.Service.store.Write(fixture.Context, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(fixture.Context, "UPDATE sessions SET closed_at=1 WHERE parent=?", fixture.Root)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			fixture.Service.discordDirectory.Apply("CHANNEL_CREATE", json.RawMessage(`{"id":"201","guild_id":"100","name":"other","type":0}`))
			var m discord.Message
			if err := json.Unmarshal([]byte(`{"id":"300","channel_id":"200","guild_id":"100","content":"question","author":{"id":"2","username":"ari"}}`), &m); err != nil {
				t.Fatal(err)
			}
			if err := fixture.Service.discordInput(fixture.Context, fixture.Client, m); err != nil {
				t.Fatal(err)
			}
			if err := fixture.Service.store.Write(fixture.Context, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(fixture.Context, "UPDATE boxes SET state='ready',want='run' WHERE session=?", fixture.Root)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			start := func(turn contract.TurnID) {
				t.Helper()
				e, err := fixture.Service.event(fixture.Room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
				if err != nil {
					t.Fatal(err)
				}
				e.Time = e.Time.Add(time.Hour)
				if active, _, _, err := fixture.Service.store.Dispatch(fixture.Context, fixture.Root, turn, e, false); err != nil || active != turn {
					t.Fatalf("dispatch %s: %s %v", turn, active, err)
				}
			}
			end := func(turn contract.TurnID, jobs int) {
				t.Helper()
				out := contract.TurnOutputRequest{Turn: turn, Outcome: contract.TurnCompleted, Final: "final answer", ShellJobs: jobs}
				e, err := fixture.Service.event(fixture.Room, boxCaller(fixture.Root), contract.TurnEnded{Session: fixture.Root, Turn: turn, Outcome: out.Outcome, Final: out.Final})
				if err != nil {
					t.Fatal(err)
				}
				if err := fixture.Service.store.EndTurn(fixture.Context, fixture.Root, out, e); err != nil {
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
					if _, problem := fixture.Service.DiscordAPI(fixture.Context, boxCaller(fixture.Root), contract.DiscordAPIRequest{Method: "POST", Path: "/channels/" + tc.channel + "/messages", Body: json.RawMessage(body)}); problem != nil {
						t.Fatal(problem)
					}
				} else if tc.channel != "" {
					if _, problem := fixture.Service.DiscordSend(fixture.Context, boxCaller(fixture.Root), contract.DiscordSendRequest{Channel: tc.channel, ReplyTo: tc.reply, Text: "explicit answer"}); problem != nil {
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
				e, err := fixture.Service.event(fixture.Room, contract.Principal{Kind: contract.PrincipalEngine}, contract.SessionNotice{Session: fixture.Root, Text: "job finished"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.Service.store.Post(fixture.Context, e, []contract.Delivery{{To: boxCaller(fixture.Root), Surface: contract.SurfaceEngine, Wake: true}}); err != nil {
					t.Fatal(err)
				}
				if err := fixture.Service.store.Write(fixture.Context, func(tx *sql.Tx) error {
					_, err := tx.ExecContext(fixture.Context, "UPDATE boxes SET state='ready',want='run' WHERE session=?", fixture.Root)
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
				reply, found, err := fixture.Service.store.NextDiscordReply(fixture.Context)
				if err != nil {
					t.Fatal(err)
				}
				if !found {
					break
				}
				if _, _, err := fixture.Client.SendMessage(fixture.Context, reply.Channel, reply.Text, reply.Message); err != nil {
					t.Fatal(err)
				}
				if err := fixture.Service.store.DiscordReplySent(fixture.Context, reply.ID, nil); err != nil {
					t.Fatal(err)
				}
			}
			if got := len(fixture.Fake.Messages); got != tc.want {
				t.Fatalf("messages = %d, want %d", got, tc.want)
			}
			var debts int
			if err := fixture.Service.store.Read().QueryRowContext(fixture.Context, "SELECT count(*) FROM replies_owed WHERE session=?", fixture.Root).Scan(&debts); err != nil {
				t.Fatal(err)
			}
			if debts != 0 {
				t.Fatal("reply debt remains")
			}
			// A late send pays nothing: the waiting turn's text already answered, and the outbox drain clears typing.
			if tc.reply == "300" && tc.channel == "200" && !tc.late {
				fixture.Service.discordRuntime.Lock()
				_, typing := fixture.Service.discordTyping["200"]
				fixture.Service.discordRuntime.Unlock()
				if typing {
					t.Fatal("typing remains after explicit reply")
				}
			}
		})
	}
}

func TestDiscordAPIPathNormalization(t *testing.T) {
	fixture := discordFixture(t)
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
			_, problem := fixture.Service.DiscordAPI(fixture.Context, boxCaller(fixture.Root), contract.DiscordAPIRequest{Method: "GET", Path: tc.path})
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
