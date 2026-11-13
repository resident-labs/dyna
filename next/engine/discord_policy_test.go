package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord"
	"github.com/resident-labs/dyna/engine/store"
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

var _ store.DiscordRoute
