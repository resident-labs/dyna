package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord"
)

func TestDiscordResumeRestoresNewThreadBeforeReplay(t *testing.T) {
	f := discordFixture(t)
	ctx, cancel := context.WithTimeout(f.ctx, 3*time.Second)
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v10/guilds/100":
			fmt.Fprint(w, `{"id":"100","name":"Horizon","roles":[{"id":"11","name":"Bot","tags":{"bot_id":"900"}}]}`)
		case "/api/v10/guilds/100/channels":
			fmt.Fprint(w, `[{"id":"200","name":"general","type":0}]`)
		case "/api/v10/guilds/100/threads/active":
			fmt.Fprint(w, `{"threads":[{"id":"201","parent_id":"200","name":"new thread","type":11}]}`)
		case "/gateway":
			c, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer c.CloseNow()
			c.Write(ctx, websocket.MessageText, []byte(`{"op":10,"d":{"heartbeat_interval":10000}}`))
			_, data, err := c.Read(ctx)
			if err != nil || !strings.Contains(string(data), `"op":6`) {
				t.Errorf("not RESUME: %s %v", data, err)
				return
			}
			c.Write(ctx, websocket.MessageText, []byte(`{"op":0,"s":56,"t":"RESUMED","d":{}}`))
			c.Write(ctx, websocket.MessageText, []byte(`{"op":0,"s":57,"t":"MESSAGE_CREATE","d":{"id":"300","guild_id":"100","channel_id":"201","content":"missed","author":{"id":"1","username":"owner"},"mention_roles":["11"],"member":{"roles":["11"]}}}`))
			<-ctx.Done()
		default:
			fmt.Fprint(w, `[]`)
		}
	}))
	defer srv.Close()
	client := &discord.Client{HTTP: srv.Client(), Endpoint: srv.URL, Token: "test-secret", BotID: "900"}
	updates := make(chan struct{})
	g := discord.Gateway{Client: client, URL: strings.Replace(srv.URL, "http:", "ws:", 1) + "/gateway", Bot: "900", Directory: &f.s.discordDirectory,
		LoadResume: func(context.Context) (discord.ResumeState, error) {
			return discord.ResumeState{Session: "saved", Seq: 55}, nil
		},
		PrepareResume: func(ctx context.Context) error { return f.s.discordRestoreDirectory(ctx, client) },
		Receive: func(ctx context.Context, m discord.Message) error {
			// Exercise role mutation beside the actual intake and directory readers.
			go func() {
				defer close(updates)
				for i := 0; i < 1000; i++ {
					f.s.discordDirectory.Apply("GUILD_ROLE_UPDATE", []byte(`{"guild_id":"100","role":{"id":"11","name":"updated","tags":{"bot_id":"900"}}}`))
				}
			}()
			for i := 0; i < 1000; i++ {
				f.s.discordDirectory.Server("100")
			}
			err := f.s.discordInput(ctx, client, m)
			<-updates
			cancel()
			return err
		},
	}
	g.Run(ctx)
	events, err := f.s.store.Events(f.ctx, f.room, 0, 100, []contract.EventType{"discord.message"})
	if err != nil || len(events) != 1 {
		t.Fatalf("replay: %d events, %v", len(events), err)
	}
	message := events[0].Body.(contract.DiscordMessage)
	if message.Thread != "201" || message.ChannelName != "new thread" {
		t.Fatalf("missing thread metadata: %+v", message)
	}
}
