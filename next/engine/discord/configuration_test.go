package discord

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConfigurationMembershipLimitedToBot(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		if request.URL.Path != "/api/v10/guilds/100/members/900" {
			t.Errorf("unexpected path %s", request.URL.Path)
		}
		response.Write([]byte(`{"roles":["10"]}`))
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), Endpoint: server.URL, Token: "token", BotID: "900"}
	if _, err := client.ReadConfiguration(context.Background(), "100", "/members/900"); err != nil {
		t.Fatal(err)
	}
	for _, resource := range []string{"/members/901", "/members", "/members/900/roles", "/channels/200/messages"} {
		if _, err := client.ReadConfiguration(context.Background(), "100", resource); err == nil {
			t.Fatalf("accepted %s", resource)
		}
	}
	if calls != 1 {
		t.Fatalf("forbidden requests escaped: %d", calls)
	}
}
