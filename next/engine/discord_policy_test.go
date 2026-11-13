package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord"
)

func TestDiscordPolicyAuthorityAndResolution(t *testing.T) {
	f := discordFixture(t)
	front := startChild(t, f.s, f.ctx, f.room, "front", f.root)
	routes, _ := f.s.store.DiscordRoutes(f.ctx, f.room)
	r := routes[0]
	r.Session = front
	if e := f.s.store.SetDiscordRoute(f.ctx, r, false); e != nil {
		t.Fatal(e)
	}
	text := "front: {roles: [Admin], quiet: [general], delete: yes}"
	if _, p := f.s.DiscordPolicy(f.ctx, boxCaller(front), contract.DiscordPolicyRequest{Text: &text}); p == nil {
		t.Fatal("front set policy")
	}
	if _, p := f.s.DiscordPolicy(f.ctx, boxCaller(f.root), contract.DiscordPolicyRequest{Text: &text}); p != nil {
		t.Fatal(p)
	}
	got, p := f.s.DiscordPolicy(f.ctx, boxCaller(front), contract.DiscordPolicyRequest{})
	if p != nil || got.Text != text {
		t.Fatalf("read %+v %v", got, p)
	}
	for _, bad := range []string{"front: {roles: [missing]}", "log: missing", "front: {quiet: [missing/*]}"} {
		if _, p := f.s.DiscordPolicy(f.ctx, boxCaller(f.root), contract.DiscordPolicyRequest{Text: &bad}); p == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	f.s.discordDirectory.Apply("GUILD_CREATE", json.RawMessage(`{"id":"100","roles":[{"id":"10","name":"Admin"},{"id":"11","name":"Admin"}]}`))
	if _, p := f.s.DiscordPolicy(f.ctx, boxCaller(f.root), contract.DiscordPolicyRequest{Text: &text}); p == nil {
		t.Fatal("ambiguous name accepted")
	}
}

func TestDiscordCapsAllWriteFamilies(t *testing.T) {
	f := discordFixture(t)
	front := startChild(t, f.s, f.ctx, f.room, "front", f.root)
	routes, _ := f.s.store.DiscordRoutes(f.ctx, f.room)
	r := routes[0]
	r.Session = front
	text := "front: {timeout_max: 1h, roles: [Admin]}"
	paths := []struct{ method, path string }{
		{"PATCH", "guilds/100/members/2"}, {"PUT", "guilds/100/members/2"}, {"DELETE", "guilds/100/members/2"}, {"PUT", "guilds/100/bans/2"}, {"DELETE", "guilds/100/bans/2"}, {"POST", "guilds/100/bulk-ban"}, {"POST", "guilds/100/prune"},
		{"POST", "guilds/100/roles"}, {"PATCH", "guilds/100/roles"}, {"PATCH", "guilds/100/roles/10"}, {"DELETE", "guilds/100/roles/10"}, {"POST", "guilds/100/channels"}, {"PATCH", "guilds/100/channels"},
		{"PATCH", "channels/200"}, {"DELETE", "channels/200"}, {"PUT", "channels/200/permissions/10"}, {"DELETE", "channels/200/permissions/10"}, {"POST", "channels/200/messages/bulk-delete"}, {"PUT", "channels/200/thread-members/@me"},
		{"POST", "channels/200/messages/300/crosspost"}, {"POST", "channels/200/followers"}, {"POST", "channels/200/polls/300/expire"}, {"POST", "channels/200/webhooks"},
		{"PATCH", "guilds/100"}, {"DELETE", "guilds/100"}, {"POST", "guilds/100/auto-moderation/rules"}, {"POST", "guilds/100/invites"}, {"POST", "channels/200/invites"}, {"DELETE", "guilds/100/integrations/2"}, {"POST", "guilds/100/scheduled-events"}, {"POST", "guilds/100/emojis"}, {"POST", "guilds/100/stickers"}, {"POST", "guilds/100/soundboard-sounds"}, {"PUT", "guilds/100/onboarding"}, {"PATCH", "guilds/100/welcome-screen"}, {"PUT", "guilds/100/incident-actions"}, {"PATCH", "guilds/100/voice-states/@me"}, {"POST", "guilds/100/future-route"},
	}
	for _, state := range []string{"none", "on", "off"} {
		r.Policy = ""
		r.PolicyResolved = ""
		if e := f.s.store.SetDiscordRoute(f.ctx, r, false); e != nil {
			t.Fatal(e)
		}
		if state != "none" {
			value := text
			if state == "off" {
				value = "enabled: false\n" + value
			}
			if _, p := f.s.DiscordPolicy(f.ctx, boxCaller(f.root), contract.DiscordPolicyRequest{Text: &value}); p != nil {
				t.Fatal(p)
			}
		}
		for _, tc := range paths {
			for _, who := range []contract.SessionID{front, f.root} {
				t.Run(fmt.Sprintf("%s/%s/%s/%s", state, who, tc.method, tc.path), func(t *testing.T) {
					p := f.s.discordScope(f.ctx, boxCaller(who), tc.method, "/api/v10/"+tc.path, json.RawMessage(`{}`))
					denied := state == "on" && who == front
					if (p != nil) != denied {
						t.Fatalf("denied=%v got %v", denied, p)
					}
				})
			}
		}
	}
}

func TestDiscordPolicyIntake(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		webhook    string
		kind       int
		want       int
	}{
		{"quiet", "front: {quiet: [general]}", "", 0, 0}, {"webhook", "", "123", 0, 0}, {"automod", "", "", 24, 0}, {"member budget", "front: {answers: {per_member: 1/h}}", "", 0, 1}, {"channel budget", "front: {answers: {per_channel: 1/h}}", "", 0, 1}, {"disabled", "enabled: false\nfront: {quiet: [general]}", "", 0, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := discordFixture(t)
			if tc.text != "" {
				if _, p := f.s.DiscordPolicy(f.ctx, boxCaller(f.root), contract.DiscordPolicyRequest{Text: &tc.text}); p != nil {
					t.Fatal(p)
				}
			}
			for i := 0; i < 2; i++ {
				var m discord.Message
				m.ID = fmt.Sprint(300 + i)
				m.Guild = "100"
				m.Channel = "200"
				m.Author.ID = "2"
				m.Content = "hello"
				m.Webhook = tc.webhook
				m.Type = tc.kind
				if e := f.s.discordInput(f.ctx, f.client, m); e != nil {
					t.Fatal(e)
				}
			}
			events, e := f.s.store.Events(f.ctx, f.room, 0, 100, []contract.EventType{"discord.message"})
			if e != nil || len(events) != tc.want {
				t.Fatalf("events %d want %d: %v", len(events), tc.want, e)
			}
		})
	}
}

func TestDiscordPolicyCheckLowBot(t *testing.T) {
	f := discordFixture(t)
	f.s.discordDirectory.Apply("GUILD_CREATE", json.RawMessage(`{"id":"100","roles":[{"id":"10","name":"Contributors","position":10}]}`))
	text := "front: {roles: [Contributors]}"
	if _, p := f.s.DiscordPolicy(f.ctx, boxCaller(f.root), contract.DiscordPolicyRequest{Text: &text}); p != nil {
		t.Fatal(p)
	}
	out, p := f.s.DiscordCheck(f.ctx, boxCaller(f.root), contract.DiscordCheckRequest{})
	if p != nil || !strings.Contains(strings.Join(out.Lines, "\n"), "above Contributors") {
		t.Fatalf("%+v %v", out, p)
	}
}

func TestDiscordPolicySendAndOutboxUseSafeMentions(t *testing.T) {
	f := discordFixture(t)
	front := startChild(t, f.s, f.ctx, f.room, "front", f.root)
	routes, _ := f.s.store.DiscordRoutes(f.ctx, f.room)
	r := routes[0]
	r.Session = front
	if e := f.s.store.SetDiscordRoute(f.ctx, r, false); e != nil {
		t.Fatal(e)
	}
	text := "front: {delete: no}"
	if _, p := f.s.DiscordPolicy(f.ctx, boxCaller(f.root), contract.DiscordPolicyRequest{Text: &text}); p != nil {
		t.Fatal(p)
	}
	if _, p := f.s.DiscordAPI(f.ctx, boxCaller(front), contract.DiscordAPIRequest{Method: "POST", Path: "/channels/200/messages", Body: json.RawMessage(`{"content":"@everyone","allowed_mentions":{"parse":["everyone"]}}`)}); p == nil {
		t.Fatal("API bypassed mentions cap")
	}
	if _, p := f.s.DiscordSend(f.ctx, boxCaller(front), contract.DiscordSendRequest{Channel: "200", Text: "@everyone"}); p != nil {
		t.Fatal(p)
	}
	check := func() {
		t.Helper()
		select {
		case body := <-f.fake.Messages:
			if !strings.Contains(string(body), `"parse":["users"]`) {
				t.Fatalf("unsafe mentions: %s", body)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("no message")
		}
	}
	check()
	if e := f.s.store.Write(f.ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(f.ctx, `INSERT INTO discord_outbox(room,session,channel,message,text) VALUES(?,?,?,?,?)`, f.room, front, "200", "300", "@everyone")
		return e
	}); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() { defer close(done); f.s.discordDrain(ctx, f.client) }()
	defer func() { cancel(); <-done }()
	check()
}

func TestDiscordAnswerBudgetIncludesDMs(t *testing.T) {
	f := discordFixture(t)
	text := "front: {answers: {per_member: 1/h}}"
	if _, p := f.s.DiscordPolicy(f.ctx, boxCaller(f.root), contract.DiscordPolicyRequest{Text: &text}); p != nil {
		t.Fatal(p)
	}
	routes, _ := f.s.store.DiscordRoutes(f.ctx, f.room)
	var m discord.Message
	m.Author.ID = "2"
	m.Channel = "200"
	m.Guild = "100"
	m.ID = "300"
	if allowed, e := f.s.discordPolicyInput(f.ctx, f.client, m, routes, routes[0]); e != nil || !allowed {
		t.Fatalf("initial message %v %v", allowed, e)
	}
	m.Guild = ""
	m.Channel = "dm"
	m.ID = "301"
	if allowed, e := f.s.discordPolicyInput(f.ctx, f.client, m, routes, routes[0]); e != nil || allowed {
		t.Fatalf("DM escaped member budget %v %v", allowed, e)
	}
}

func TestDiscordPolicyResolvesLiveChannelNames(t *testing.T) {
	f := discordFixture(t)
	f.s.discordDirectory.Apply("CHANNEL_UPDATE", json.RawMessage(`{"id":"200","guild_id":"100","name":"renamed"}`))
	old := "front: {quiet: [general]}"
	if _, p := f.s.DiscordPolicy(f.ctx, boxCaller(f.root), contract.DiscordPolicyRequest{Text: &old}); p == nil {
		t.Fatal("stale channel name accepted")
	}
	updated := "front: {quiet: [renamed]}"
	if _, p := f.s.DiscordPolicy(f.ctx, boxCaller(f.root), contract.DiscordPolicyRequest{Text: &updated}); p != nil {
		t.Fatal(p)
	}
}

func TestDiscordPolicyAllowsSendFiles(t *testing.T) {
	f := discordFixture(t)
	front := startChild(t, f.s, f.ctx, f.room, "front", f.root)
	routes, _ := f.s.store.DiscordRoutes(f.ctx, f.room)
	route := routes[0]
	route.Session = front
	if err := f.s.store.SetDiscordRoute(f.ctx, route, false); err != nil {
		t.Fatal(err)
	}
	text := "front: {}"
	if _, problem := f.s.DiscordPolicy(f.ctx, boxCaller(f.root), contract.DiscordPolicyRequest{Text: &text}); problem != nil {
		t.Fatal(problem)
	}
	f.s.SetLayout(box.Layout{Data: t.TempDir()})
	hash, size, err := f.s.saveFile(f.ctx, f.room, strings.NewReader("file contents"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, problem := f.s.DiscordSend(f.ctx, boxCaller(front), contract.DiscordSendRequest{Channel: "200", Text: "attached", Attachments: []contract.Attachment{{Name: "file.txt", SHA256: hash, Bytes: size}}}); problem != nil {
		t.Fatal(problem)
	}
	select {
	case name := <-f.fake.UploadedFiles:
		if name != "file.txt" {
			t.Fatalf("wrong uploaded file: %s", name)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no multipart message")
	}
}

func TestDiscordPolicyForwardsRichMessagesAndSuppressedMentions(t *testing.T) {
	f := discordFixture(t)
	front := startChild(t, f.s, f.ctx, f.room, "front", f.root)
	routes, _ := f.s.store.DiscordRoutes(f.ctx, f.room)
	route := routes[0]
	route.Session = front
	if err := f.s.store.SetDiscordRoute(f.ctx, route, false); err != nil {
		t.Fatal(err)
	}
	text := "front: {timeout_max: 1h}"
	if _, problem := f.s.DiscordPolicy(f.ctx, boxCaller(f.root), contract.DiscordPolicyRequest{Text: &text}); problem != nil {
		t.Fatal(problem)
	}
	for _, body := range []string{`{"embeds":[{"title":"Status"}]}`, `{"poll":{"question":{"text":"Ready?"},"duration":24}}`, `{"components":[{"type":1,"components":[{"type":2,"style":5,"label":"Docs","url":"https://example.com"}]}]}`} {
		if _, problem := f.s.DiscordAPI(f.ctx, boxCaller(front), contract.DiscordAPIRequest{Method: "POST", Path: "/channels/200/messages", Body: json.RawMessage(body)}); problem != nil {
			t.Fatal(problem)
		}
		select {
		case forwarded := <-f.fake.Messages:
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(forwarded, &fields); err != nil {
				t.Fatal(err)
			}
			if string(fields["allowed_mentions"]) != `{"parse":[]}` {
				t.Fatalf("mentions not suppressed: %s", forwarded)
			}
			for _, key := range []string{"embeds", "poll", "components"} {
				if strings.Contains(body, `"`+key+`"`) && len(fields[key]) == 0 {
					t.Fatalf("lost rich field %s", forwarded)
				}
			}
		case <-time.After(3 * time.Second):
			t.Fatal("no forwarded body")
		}
	}
	for _, body := range []string{`{"tts":true}`, `{"attachments":[]}`, `{"flags":64}`, `{"allowed_mentions":{"parse":["everyone"]}}`} {
		if _, problem := f.s.DiscordAPI(f.ctx, boxCaller(front), contract.DiscordAPIRequest{Method: "POST", Path: "/channels/200/messages", Body: json.RawMessage(body)}); problem == nil || problem.Code != contract.ErrForbidden {
			t.Fatalf("permission refusal missing for %s: %v", body, problem)
		}
	}
	tooLong := time.Now().Add(2 * time.Hour).Format(time.RFC3339)
	_, problem := f.s.DiscordAPI(f.ctx, boxCaller(front), contract.DiscordAPIRequest{Method: "PATCH", Path: "/guilds/100/members/2", Body: json.RawMessage(`{"communication_disabled_until":"` + tooLong + `"}`)})
	if problem == nil || problem.Code != contract.ErrForbidden || problem.Message != "the server's policy lets the answering session time a member out for at most 1h; ask a moderator or your parent" {
		t.Fatalf("timeout refusal: %v", problem)
	}
	f.fake.SetHistory([]any{map[string]any{"id": "300", "author": map[string]string{"id": "900"}}, map[string]any{"id": "301", "author": map[string]string{"id": "2"}}})
	for _, messageID := range []string{"300", "301"} {
		_, problem := f.s.DiscordAPI(f.ctx, boxCaller(front), contract.DiscordAPIRequest{Method: "POST", Path: "/channels/200/polls/" + messageID + "/expire"})
		if (problem == nil) != (messageID == "300") {
			t.Fatalf("poll %s: %v", messageID, problem)
		}
	}
}
