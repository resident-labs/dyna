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
		{"POST", "channels/3/messages", `{"content":"hi"}`, false},
		{"PATCH", "channels/3/messages/5", `{"content":"hi","allowed_mentions":{"parse":[]}}`, true},
		{"POST", "channels/3/messages/bulk-delete", `{}`, false},
		{"POST", "channels/3/threads", `{"name":"test"}`, true},
		{"POST", "channels/3/messages/5/threads", `{"name":"test"}`, true},
		{"PUT", "channels/3/thread-members/@me", `{}`, false},
		{"PUT", "channels/3/messages/5/reactions/x/@me", `{}`, true},
		{"PUT", "channels/3/messages/pins/5", `{}`, true},
		{"POST", "channels/3/messages/5/crosspost", `{}`, false},
		{"POST", "channels/3/followers", `{}`, false},
		{"POST", "channels/3/polls/5/expire", `{}`, false},
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
