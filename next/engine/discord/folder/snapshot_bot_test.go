package folder

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/resident-labs/dyna/engine/discord"
)

func TestBotPositionIsPartOfSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v10/guilds/100/members/900" {
			t.Errorf("unexpected read %s", request.URL.Path)
		}
		response.Write([]byte(`{"roles":["10","11"]}`))
	}))
	defer server.Close()
	client := &discord.Client{HTTP: server.Client(), Endpoint: server.URL, Token: "token", BotID: "900"}
	highest, err := snapshotBotPosition(context.Background(), client, "100", []Role{{Identity: Identity{ID: "10"}, Position: 2}, {Identity: Identity{ID: "11"}, Position: 5}, {Identity: Identity{ID: "12"}, Position: 20}})
	if err != nil || highest != 5 {
		t.Fatalf("highest role %d, %v", highest, err)
	}
}
