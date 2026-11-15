package discord

import (
	"context"
	"fmt"
	"github.com/coder/websocket"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReviewFatalCloseMustStop(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		defer c.CloseNow()
		attempts.Add(1)
		c.Write(r.Context(), websocket.MessageText, []byte(`{"op":10,"d":{"heartbeat_interval":1000}}`))
		c.Read(r.Context())
		c.Close(websocket.StatusCode(4014), "disallowed intents")
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	g := Gateway{Client: &Client{HTTP: srv.Client(), Token: "x"}, URL: strings.Replace(srv.URL, "http:", "ws:", 1)}
	g.Run(ctx)
	if attempts.Load() != 1 {
		t.Fatalf("fatal 4014 retried: %d connections", attempts.Load())
	}
}
func TestReviewInvalidSequenceMustReidentify(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		defer c.CloseNow()
		c.Write(r.Context(), websocket.MessageText, []byte(`{"op":10,"d":{"heartbeat_interval":1000}}`))
		c.Read(r.Context())
		c.Close(websocket.StatusCode(4007), "invalid sequence")
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	session, resume, seq := "old", "", int64(55)
	g := Gateway{Client: &Client{HTTP: srv.Client(), Token: "x"}, URL: strings.Replace(srv.URL, "http:", "ws:", 1)}
	g.connect(ctx, &session, &resume, &seq)
	if session != "" {
		t.Fatal("4007 preserved rejected resume state; every reconnect sends same invalid resume")
	}
}

func TestRestartLoadsResumeAndReplays(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ops := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		defer c.CloseNow()
		c.Write(ctx, websocket.MessageText, []byte(`{"op":10,"d":{"heartbeat_interval":10000}}`))
		_, data, _ := c.Read(ctx)
		ops <- string(data)
		c.Write(ctx, websocket.MessageText, []byte(`{"op":0,"s":56,"t":"RESUMED","d":{}}`))
		c.Write(ctx, websocket.MessageText, []byte(`{"op":0,"s":57,"t":"MESSAGE_CREATE","d":{"id":"missed","channel_id":"dm","author":{"id":"human"}}}`))
		c.Write(ctx, websocket.MessageText, []byte(`{"op":0,"s":57,"t":"MESSAGE_CREATE","d":{"id":"missed","channel_id":"dm","author":{"id":"human"}}}`))
		<-ctx.Done()
	}))
	defer srv.Close()
	received := 0
	g := Gateway{Client: &Client{HTTP: srv.Client(), Token: "x"}, URL: strings.Replace(srv.URL, "http:", "ws:", 1), LoadResume: func(context.Context) (ResumeState, error) { return ResumeState{Session: "saved", Seq: 55}, nil }, Receive: func(_ context.Context, m Message) error { received++; cancel(); return nil }, CatchUp: func(context.Context, string, func(context.Context, Message) error) { t.Error("catch-up after resume") }}
	g.Run(ctx)
	if op := <-ops; !strings.Contains(op, `"op":6`) || !strings.Contains(op, `"seq":55`) {
		t.Fatal(op)
	}
	if received != 1 {
		t.Fatalf("received %d", received)
	}
}

func TestInvalidSessionEnablesFreshCatchUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var invalid atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		defer c.CloseNow()
		c.Write(ctx, websocket.MessageText, []byte(`{"op":10,"d":{"heartbeat_interval":10000}}`))
		c.Read(ctx)
		if !invalid.Swap(true) {
			c.Write(ctx, websocket.MessageText, []byte(`{"op":9,"d":false}`))
			return
		}
		c.Write(ctx, websocket.MessageText, []byte(`{"op":0,"s":1,"t":"READY","d":{"session_id":"new"}}`))
		c.Write(ctx, websocket.MessageText, []byte(`{"op":0,"s":2,"t":"GUILD_CREATE","d":{"id":"guild"}}`))
		<-ctx.Done()
	}))
	defer srv.Close()
	g := Gateway{Client: &Client{HTTP: srv.Client(), Token: "secret"}, URL: strings.Replace(srv.URL, "http:", "ws:", 1), CatchUp: func(_ context.Context, guild string, _ func(context.Context, Message) error) {
		if guild != "guild" {
			t.Error(guild)
		}
		cancel()
	}}
	session, resume, seq := "old", "", int64(20)
	g.connect(ctx, &session, &resume, &seq)
	if session != "" || seq != 0 {
		t.Fatal("invalid state retained")
	}
	g.connect(ctx, &session, &resume, &seq)
	if session != "new" {
		t.Fatal("fresh READY not handled")
	}
}

func TestPrepareResumeOnceAndFailureDoesNotBlockConnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	var connections atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.Write(ctx, websocket.MessageText, []byte(`{"op":10,"d":{"heartbeat_interval":10000}}`))
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		if !strings.Contains(string(data), `"op":6`) {
			t.Errorf("not RESUME: %s", data)
		}
		if connections.Add(1) == 1 {
			c.Write(ctx, websocket.MessageText, []byte(`{"op":7,"d":null}`))
			return
		}
		cancel()
	}))
	defer srv.Close()
	calls := 0
	g := Gateway{Client: &Client{HTTP: srv.Client(), Token: "secret"}, URL: strings.Replace(srv.URL, "http:", "ws:", 1), LoadResume: func(context.Context) (ResumeState, error) { return ResumeState{Session: "saved", Seq: 55}, nil }, PrepareResume: func(context.Context) error { calls++; return fmt.Errorf("unavailable guild") }}
	g.Run(ctx)
	if calls != 1 || connections.Load() != 2 {
		t.Fatalf("prepare %d, connections %d", calls, connections.Load())
	}
}
