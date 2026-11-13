package engine

import (
	"database/sql"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord"
	"testing"
)

func TestDiscordFrontGuard(t *testing.T) {
	for _, tc := range []struct {
		name, policy    string
		roles           []string
		owner, confined bool
		legacy          bool
	}{
		{name: "legacy DM", policy: "front:\n  team: [Admin]", confined: true, legacy: true},
		{name: "guest", policy: "front:\n  team: [Admin]", confined: true},
		{name: "empty team", policy: "front:\n  team: []", confined: true},
		{name: "team", policy: "front:\n  team: [Admin]", roles: []string{"10"}},
		{name: "no key", policy: "front: {}"},
		{name: "owner DM", policy: "front:\n  team: [Admin]", owner: true},
		{name: "owner", policy: "front:\n  team: [Admin]", owner: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := discordFixture(t)
			if _, p := f.s.DiscordPolicy(f.ctx, boxCaller(f.root), contract.DiscordPolicyRequest{Text: &tc.policy}); p != nil {
				t.Fatal(p)
			}
			m := discord.Message{ID: "400", Channel: "200", Guild: "100"}
			m.Author.ID = "2"
			if tc.owner {
				m.Author.ID = "1"
				if tc.name == "owner DM" {
					m.Guild = ""
					m.Channel = "500"
				}
			}
			m.Member.Roles = tc.roles
			if err := f.s.discordInput(f.ctx, f.client, m); err != nil {
				t.Fatal(err)
			}
			events, err := f.s.store.Events(f.ctx, f.room, 0, 100, []contract.EventType{"discord.message"})
			if err != nil || len(events) != 1 {
				t.Fatal(events, err)
			}
			if got := events[0].Body.(contract.DiscordMessage).Confined; got != tc.confined {
				t.Fatalf("confined %v", got)
			}
			if err := f.s.store.Write(f.ctx, func(tx *sql.Tx) error {
				if tc.legacy {
					if _, err := tx.ExecContext(f.ctx, "UPDATE events SET body=json_remove(json_set(CAST(body AS TEXT),'$.direct',json('true')),'$.confined') WHERE type='discord.message'"); err != nil {
						return err
					}
				}
				if _, err := tx.ExecContext(f.ctx, "INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,started_seq) VALUES('guard',?,0,'','',0,0,0,0,0)", f.root); err != nil {
					return err
				}
				_, err := tx.ExecContext(f.ctx, "UPDATE deliveries SET turn='guard',state='taken' WHERE to_id=?", f.root)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			caller := boxCaller(f.root)
			for _, path := range []string{"/api/v10/guilds/100/audit-logs", "/api/v10/guilds/100/members/2", "/api/v10/users/2"} {
				p := f.s.discordScope(f.ctx, caller, "GET", path)
				if (p != nil) != tc.confined {
					t.Fatalf("%s: %v", path, p)
				}
			}
			if !tc.confined {
				return
			}
			before := f.fake.Requests()
			for _, call := range []func() *contract.Error{
				func() *contract.Error {
					_, p := f.s.DiscordExport(f.ctx, caller, contract.DiscordExportRequest{})
					return p
				},
				func() *contract.Error {
					_, p := f.s.DiscordPlan(f.ctx, caller, contract.DiscordPlanRequest{})
					return p
				},
				func() *contract.Error {
					_, p := f.s.DiscordApply(f.ctx, caller, contract.DiscordApplyRequest{})
					return p
				},
				func() *contract.Error {
					_, p := f.s.DiscordCheck(f.ctx, caller, contract.DiscordCheckRequest{})
					return p
				},
			} {
				if p := call(); p == nil || p.Message != confinedDiscordRefusal().Message {
					t.Fatal("unconfined call", p)
				}
			}
			if f.fake.Requests() != before {
				t.Fatal("confined tool called Discord")
			}
			for _, path := range []string{"/api/v10/channels/201/messages", "/api/v10/channels/200/pins"} {
				if p := f.s.discordScope(f.ctx, caller, "GET", path); p == nil {
					t.Fatal("read allowed", path)
				}
			}
			if _, p := f.s.DiscordRead(f.ctx, caller, contract.DiscordReadRequest{Channel: "201"}); p == nil {
				t.Fatal("other channel read")
			}
			if _, p := f.s.DiscordSend(f.ctx, caller, contract.DiscordSendRequest{Channel: "201", Text: "no"}); p == nil {
				t.Fatal("other channel send")
			}
			sent, p := f.s.DiscordSend(f.ctx, caller, contract.DiscordSendRequest{Channel: "200", Text: "answer"})
			if p != nil {
				t.Fatal(p)
			}
			if len(sent.Messages) != 1 {
				t.Fatal(sent)
			}
			f.fake.SetHistory([]any{map[string]any{"id": sent.Messages[0], "author": map[string]string{"id": "900"}}})
			if p := f.s.discordScope(f.ctx, caller, "DELETE", "/api/v10/channels/200/messages/"+sent.Messages[0]); p != nil {
				t.Fatal(p)
			}
			if p := f.s.discordScope(f.ctx, caller, "GET", "/api/v10/channels/200/messages"); p != nil {
				t.Fatal(p)
			}
		})
	}
}
