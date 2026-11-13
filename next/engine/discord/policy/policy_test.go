package policy

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStrictDecode(t *testing.T) {
	for _, text := range []string{"rules: []", "front: {ban: never}", "front: {delete: yes, delete: no}", "front: &x {delete: yes}\nlog: *x", "front: {timeout_max: 900h}", "front: {answers: {per_member: 2/m}}", "enabled: true\n---\nenabled: false"} {
		if _, e := Decode(text); e == nil {
			t.Errorf("accepted %s", text)
		}
	}
	p, e := Decode("enabled: true\nlog: mod-log\nfront:\n  timeout_max: 1h\n  delete: yes\n  threads: no\n  roles: [Contributors]\n  quiet: [Logs/*]\n  answers: {per_member: 6/h, per_channel: 30/h}\n")
	if e != nil || !p.On() || !bool(p.Front.Delete) {
		t.Fatalf("%+v %v", p, e)
	}
}
func TestWriteMatrix(t *testing.T) {
	p := Policy{Front: Front{TimeoutMax: "1h", Roles: []string{"4"}, Delete: true, Threads: true}}
	f := Facts{Now: time.Now(), OwnMessage: true}
	for _, tc := range []struct {
		method, path, body string
		allow              bool
	}{
		{"PATCH", "guilds/1/members/2", `{"communication_disabled_until":null}`, true},
		{"PATCH", "guilds/1/members/2", `{"communication_disabled_until":null,"roles":["4"]}`, false},
		{"PUT", "guilds/1/members/2/roles/4", `{}`, true},
		{"DELETE", "guilds/1/members/2/roles/5", `{}`, false},
		{"PUT", "guilds/1/members/2", `{}`, false},
		{"DELETE", "guilds/1/members/2", `{}`, false},
		{"PUT", "guilds/1/bans/2", `{}`, false},
		{"DELETE", "guilds/1/bans/2", `{}`, false},
		{"POST", "guilds/1/bulk-ban", `{}`, false},
		{"POST", "guilds/1/prune", `{}`, false},
		{"POST", "guilds/1/roles", `{}`, false},
		{"PATCH", "guilds/1/roles/4", `{}`, false},
		{"POST", "guilds/1/channels", `{}`, false},
		{"PATCH", "channels/3", `{}`, false},
		{"DELETE", "channels/3", `{}`, false},
		{"PUT", "channels/3/permissions/4", `{}`, false},
		{"POST", "channels/3/messages", `{"content":"hi","allowed_mentions":{"parse":["users"]}}`, true},
		{"POST", "channels/3/messages", `{"content":"hi"}`, true},
		{"PATCH", "channels/3/messages/5", `{"content":"hi","allowed_mentions":{"parse":[]}}`, true},
		{"POST", "channels/3/messages/bulk-delete", `{}`, false},
		{"POST", "channels/3/threads", `{"name":"test"}`, true},
		{"POST", "channels/3/messages/5/threads", `{"name":"test"}`, true},
		{"PUT", "channels/3/thread-members/@me", `{}`, false},
		{"PUT", "channels/3/messages/5/reactions/x/@me", `{}`, true},
		{"PUT", "channels/3/messages/pins/5", `{}`, true},
		{"POST", "channels/3/messages/5/crosspost", `{}`, false},
		{"POST", "channels/3/followers", `{}`, false},
		{"POST", "channels/3/polls/5/expire", `{}`, true},
		{"POST", "channels/3/webhooks", `{}`, false},
		{"GET", "channels/3/webhooks", `{}`, false},
		{"GET", "guilds/1/webhooks", `{}`, false},
		{"POST", "guilds/1/auto-moderation/rules", `{}`, false},
		{"POST", "guilds/1/invites", `{}`, false},
		{"POST", "channels/3/invites", `{}`, false},
		{"DELETE", "guilds/1/integrations/2", `{}`, false},
		{"POST", "guilds/1/scheduled-events", `{}`, false},
		{"POST", "guilds/1/emojis", `{}`, false},
		{"POST", "guilds/1/stickers", `{}`, false},
		{"POST", "guilds/1/soundboard-sounds", `{}`, false},
		{"PUT", "guilds/1/onboarding", `{}`, false},
		{"PATCH", "guilds/1/welcome-screen", `{}`, false},
		{"PUT", "guilds/1/incident-actions", `{}`, false},
		{"PATCH", "guilds/1/voice-states/@me", `{}`, false},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			e := p.Write(tc.method, "/api/v10/"+tc.path, json.RawMessage(tc.body), f)
			if (e == nil) != tc.allow {
				t.Fatalf("allow=%v: %v", tc.allow, e)
			}
		})
	}
	f.RolePermissions = 1 << 3
	if p.Write("PUT", "/api/v10/guilds/1/members/2/roles/4", nil, f) == nil {
		t.Fatal("administrator role accepted")
	}
	until := f.Now.Add(2 * time.Hour).Format(time.RFC3339)
	if p.Write("PATCH", "/api/v10/guilds/1/members/2", json.RawMessage(`{"communication_disabled_until":"`+until+`"}`), f) == nil {
		t.Fatal("long timeout accepted")
	}
	_, e := Decode("commands: []")
	if e == nil || !strings.Contains(e.Error(), "not supported yet") {
		t.Fatal(e)
	}
}

func TestJSONCaseAndDuplicatesCannotShadowCaps(t *testing.T) {
	p := Policy{Front: Front{TimeoutMax: "1h"}}
	for _, body := range []string{
		`{"content":"@everyone","allowed_mentions":{"parse":["everyone"]},"ALLOWED_MENTIONS":{"parse":["users"]}}`,
		`{"allowed_mentions":{"parse":["everyone"],"parse":["users"]}}`,
		`{"allowed_mentions":{"PARSE":["users"]}}`,
	} {
		if e := p.Write("POST", "/api/v10/channels/3/messages", json.RawMessage(body), Facts{Now: time.Now()}); e == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	until := time.Now().Add(2 * time.Hour).Format(time.RFC3339)
	for _, body := range []string{`{"communication_disabled_until":"` + until + `","COMMUNICATION_DISABLED_UNTIL":null}`, `{"communication_disabled_until":"` + until + `","communication_disabled_until":null}`} {
		if e := p.Write("PATCH", "/api/v10/guilds/1/members/2", json.RawMessage(body), Facts{Now: time.Now()}); e == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

func TestRichMessagesAndOwnReactions(t *testing.T) {
	caps := Policy{}
	for _, body := range []string{`{"embeds":[{"title":"Build","fields":[{"name":"Status","value":"green"}]}]}`, `{"poll":{"question":{"text":"Ready?"},"answers":[{"poll_media":{"text":"Yes"}}],"duration":24}}`, `{"components":[{"type":1,"components":[{"type":2,"style":5,"label":"Docs","url":"https://example.com"}]}]}`, `{"sticker_ids":["123"],"flags":4100}`} {
		for _, action := range []Action{MessageCreate, MessageEdit} {
			normalized, err := caps.Authorize(Route{Action: action}, json.RawMessage(body), Facts{OwnMessage: true})
			if err != nil || !strings.Contains(string(normalized), `"allowed_mentions":{"parse":[]}`) {
				t.Fatalf("%s: %s %v", action, normalized, err)
			}
		}
	}
	for _, body := range []string{`{"tts":true}`, `{"attachments":[]}`, `{"flags":64}`, `{"flags":4097}`, `{"embeds":[null]}`, `{"poll":[]}`, `{"components":[1]}`, `{"embeds":[{},{},{},{},{},{},{},{},{},{},{}]}`} {
		if err := caps.Write("POST", "/api/v10/channels/3/messages", json.RawMessage(body), Facts{}); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	for _, path := range []string{"/api/v10/channels/3/messages/5/reactions/x/@me", "/api/v10/channels/3/messages/5/reactions/x/2", "/api/v10/channels/3/messages/5/reactions/x", "/api/v10/channels/3/messages/5/reactions"} {
		for _, method := range []string{"PUT", "DELETE"} {
			err := caps.Write(method, path, nil, Facts{})
			if (err == nil) != strings.HasSuffix(path, "/@me") {
				t.Fatalf("%s %s: %v", method, path, err)
			}
		}
	}
	for _, own := range []bool{false, true} {
		err := caps.Write("POST", "/api/v10/channels/3/polls/5/expire", nil, Facts{OwnMessage: own})
		if (err == nil) != own {
			t.Fatalf("poll own=%v: %v", own, err)
		}
	}
}

func TestMessageNormalizationPreservesClearingEdits(t *testing.T) {
	body := json.RawMessage(`{"content":"","embeds":[],"components":[],"flags":0}`)
	normalized, err := (Policy{}).Authorize(Route{Action: MessageEdit}, body, Facts{OwnMessage: true})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(normalized, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"content", "embeds", "components", "flags", "allowed_mentions"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("lost clearing field %s: %s", key, normalized)
		}
	}
}

func TestChannelChores(t *testing.T) {
	caps := Policy{Front: Front{Channels: []string{"topic", "slowmode", "create"}}}
	facts := Facts{CategoryInGuild: func(id string) bool { return id == "category" }}
	for _, test := range []struct {
		method, path, body string
		allowed            bool
	}{
		{"PATCH", "/api/v10/channels/3", `{"topic":"hello"}`, true},
		{"PATCH", "/api/v10/channels/3", `{"topic":""}`, true},
		{"PATCH", "/api/v10/channels/3", `{"topic":"hello","name":"other"}`, false},
		{"PATCH", "/api/v10/channels/3", `{"topic":"hello","rate_limit_per_user":null}`, false},
		{"PATCH", "/api/v10/channels/3", `{"rate_limit_per_user":0}`, true},
		{"PATCH", "/api/v10/channels/3", `{"rate_limit_per_user":21600}`, true},
		{"PATCH", "/api/v10/channels/3", `{"rate_limit_per_user":21601}`, false},
		{"PATCH", "/api/v10/channels/3", `{"rate_limit_per_user":-1}`, false},
		{"PATCH", "/api/v10/channels/3", `{"rate_limit_per_user":null}`, false},
		{"PATCH", "/api/v10/channels/3", `{"rate_limit_per_user":1,"topic":"extra"}`, false},
		{"PATCH", "/api/v10/channels/3", `{"rate_limit_per_user":1,"permission_overwrites":[]}`, false},
		{"POST", "/api/v10/guilds/1/channels", `{"name":"help"}`, true},
		{"POST", "/api/v10/guilds/1/channels", `{"name":"help","topic":"questions","type":0,"parent_id":"category"}`, true},
		{"POST", "/api/v10/guilds/1/channels", `{"name":"help","parent_id":"other-server"}`, false},
		{"POST", "/api/v10/guilds/1/channels", `{"name":"help","type":2}`, false},
		{"POST", "/api/v10/guilds/1/channels", `{"name":"help","type":null}`, false},
		{"POST", "/api/v10/guilds/1/channels", `{"name":"help","parent_id":null}`, false},
		{"POST", "/api/v10/guilds/1/channels", `{"name":"help","permission_overwrites":[]}`, false},
		{"PATCH", "/api/v10/guilds/1/channels", `{"topic":"hello"}`, false},
	} {
		err := caps.Write(test.method, test.path, json.RawMessage(test.body), facts)
		if (err == nil) != test.allowed {
			t.Errorf("%s %s %s: %v", test.method, test.path, test.body, err)
		}
		if test.allowed && (Policy{}).Write(test.method, test.path, json.RawMessage(test.body), facts) == nil {
			t.Errorf("allowed without chore: %s", test.body)
		}
	}
	for _, text := range []string{"front: {channels: [topic, slowmode, create]}", "front: {channels: []}"} {
		if _, err := Decode(text); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Decode("front: {channels: [permissions]}"); err == nil {
		t.Fatal("unknown chore accepted")
	}
}

func TestRefusalsNameActionClasses(t *testing.T) {
	for _, test := range []struct{ method, path, phrase string }{
		{"PUT", "guilds/1/bans/2", "ban or unban"}, {"DELETE", "guilds/1/members/2", "kick"},
		{"POST", "channels/3/messages/bulk-delete", "bulk-delete"}, {"PATCH", "guilds/1/roles/4", "roles"},
		{"PATCH", "channels/3", "channel edit"}, {"PUT", "channels/3/permissions/4", "permission overwrites"},
		{"POST", "channels/3/webhooks", "webhooks"}, {"PATCH", "guilds/1", "server settings"},
		{"DELETE", "channels/3/messages/5/reactions/x/2", "other members' reactions"},
		{"DELETE", "channels/3/messages/5/reactions", "other members' reactions"},
		{"POST", "channels/3/future-route", "this action"},
	} {
		err := (Policy{}).Write(test.method, "/api/v10/"+test.path, nil, Facts{})
		if err == nil || !strings.Contains(err.Error(), test.phrase) || !strings.HasSuffix(err.Error(), "; ask a moderator or your parent") {
			t.Errorf("%s: %v", test.path, err)
		}
	}
}
