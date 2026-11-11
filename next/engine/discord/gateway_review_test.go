package discord

import (
	"context"
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
