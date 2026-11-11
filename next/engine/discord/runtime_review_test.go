package discord

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestDiscordSharedCooldownAndRetryAfter(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.Header().Set("Retry-After", "0.1")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"retry_after":0.1}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"999"}`))
	}))
	defer server.Close()
	c := &Client{HTTP: server.Client(), Endpoint: server.URL, Token: "token"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, status, err := c.SendMessage(ctx, "200", "text", "")
	var response *SendError
	if status != 429 || !errors.As(err, &response) || response.RetryAfter != 100*time.Millisecond {
		t.Fatalf("retry information lost: %d %v", status, err)
	}
	start := time.Now()
	result, err := c.API(ctx, "GET", "/api/v10/channels/200/messages", nil, "")
	if err != nil || result.Status != 200 {
		t.Fatalf("GET: %+v %v", result, err)
	}
	if time.Since(start) < 75*time.Millisecond {
		t.Fatal("different request ignored shared cooldown")
	}
}

func TestDiscordSlowInputDoesNotBlockHeartbeat(t *testing.T) {
	var beats atomic.Int32
	started := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		_ = c.Write(r.Context(), websocket.MessageText, []byte(`{"op":10,"d":{"heartbeat_interval":100}}`))
		if _, _, err := c.Read(r.Context()); err != nil {
			return
		}
		_ = c.Write(r.Context(), websocket.MessageText, []byte(`{"op":0,"t":"READY","s":1,"d":{"session_id":"test"}}`))
		for i := 0; i < 300; i++ {
			if err := c.Write(r.Context(), websocket.MessageText, []byte(fmt.Sprintf(`{"op":0,"t":"MESSAGE_CREATE","s":%d,"d":{"id":"%d","channel_id":"200","content":"hello","author":{"id":"3"}}}`, i+2, i+100))); err != nil {
				return
			}
		}
		for {
			_, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if strings.Contains(string(data), `"op":1`) {
				beats.Add(1)
				if err := c.Write(r.Context(), websocket.MessageText, []byte(`{"op":11}`)); err != nil {
					return
				}
			}
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	g := Gateway{Client: &Client{HTTP: server.Client(), Token: "token"}, URL: strings.Replace(server.URL, "http:", "ws:", 1), Bot: "2", Receive: func(ctx context.Context, m Message) error {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	_ = g.Run(ctx)
	if len(started) == 0 || beats.Load() < 3 {
		t.Fatalf("blocked input stalled heartbeats: started %d, beats %d", len(started), beats.Load())
	}
}
