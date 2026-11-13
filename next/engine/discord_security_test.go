package engine

import (
	"encoding/json"
	"github.com/resident-labs/dyna/contract"
	"strings"
	"testing"
)

func TestReviewDiscordCrossRoomBodies(t *testing.T) {
	fixture := discordFixture(t)
	fixture.Service.discordDirectory.Apply("GUILD_CREATE", json.RawMessage(`{"id":"101","name":"Other room","channels":[{"id":"201","name":"private"}]}`))
	for _, tc := range []struct{ name, path, body string }{
		{"follow into another server", "/api/v10/channels/200/followers", `{"webhook_channel_id":"201"}`},
		{"forward from another server", "/api/v10/channels/200/messages", `{"message_reference":{"type":1,"guild_id":"101","channel_id":"201","message_id":"555"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, problem := fixture.Service.DiscordAPI(fixture.Context, boxCaller(fixture.Root), contract.DiscordAPIRequest{Method: "POST", Path: tc.path, Body: json.RawMessage(tc.body)})
			if problem == nil {
				t.Fatal("cross-server resource body reached authenticated Discord API")
			}
		})
	}
}

func TestDiscordRefusesNoncanonicalPathsBeforeTransport(t *testing.T) {
	fixture := discordFixture(t)
	before := fixture.Fake.Requests()
	for _, path := range []string{"/api/v10/channels/200/../../guilds/101/bans", "/api/v10/channels/200/./messages", "/api/v10/channels/200//messages", "/api/v10/channels/200/%2e%2e/messages", "/api/v10/channels/200/%6dessages"} {
		if _, problem := fixture.Service.DiscordAPI(fixture.Context, boxCaller(fixture.Root), contract.DiscordAPIRequest{Method: "GET", Path: path}); problem == nil {
			t.Errorf("allowed %s", path)
		}
	}
	if fixture.Fake.Requests() != before {
		t.Fatal("noncanonical path reached transport")
	}
}
func TestDiscordResourceReferencesInBodiesAndQueries(t *testing.T) {
	fixture := discordFixture(t)
	before := fixture.Fake.Requests()
	for _, body := range []string{`{"channel_id":201}`, `{"channel_id":{"id":"200"}}`, `{"channel_id":["200"]}`, `{"nested":[{"target_channel_id":"201"}]}`, `{"guild_id":"101"}`} {
		if _, problem := fixture.Service.DiscordAPI(fixture.Context, boxCaller(fixture.Root), contract.DiscordAPIRequest{Method: "POST", Path: "/api/v10/channels/200/messages", Body: json.RawMessage(body)}); problem == nil {
			t.Errorf("allowed %s", body)
		}
	}
	for _, query := range []string{"guild_id=101", "webhook_channel_id=201", "channel_id=200&channel_id=201", "channel_id=%7B200%7D"} {
		if _, problem := fixture.Service.DiscordAPI(fixture.Context, boxCaller(fixture.Root), contract.DiscordAPIRequest{Method: "GET", Path: "/api/v10/channels/200/messages?" + query}); problem == nil {
			t.Errorf("allowed %s", query)
		}
	}
	if fixture.Fake.Requests() != before {
		t.Fatal("foreign resource reference reached transport")
	}
	if problem := fixture.Service.discordScope(fixture.Context, boxCaller(fixture.Root), "POST", "/api/v10/channels/200/messages?channel_id=200", json.RawMessage(`{"nested":[{"guild_id":"100","channel_id":"200"}]}`)); problem != nil {
		t.Fatal(problem)
	}
}

func TestDiscordRefusesStringBody(t *testing.T) {
	fixture := discordFixture(t)
	before := fixture.Fake.Requests()
	_, problem := fixture.Service.DiscordAPI(fixture.Context, boxCaller(fixture.Root), contract.DiscordAPIRequest{Method: "POST", Path: "/api/v10/channels/200/messages", Body: json.RawMessage(`"{}"`)})
	if problem == nil || !strings.Contains(problem.Message, "object or array") {
		t.Fatalf("got %v", problem)
	}
	if fixture.Fake.Requests() != before {
		t.Fatal("string body reached Discord")
	}
}
