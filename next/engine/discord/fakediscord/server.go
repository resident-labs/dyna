// Package fakediscord serves a token-checking Discord REST and gateway fixture.
package fakediscord

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type Server struct {
	HTTP        *httptest.Server
	Token       string
	Messages    chan json.RawMessage
	Events      chan any
	mu          sync.Mutex
	connections int
}

func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{Token: "fake-discord-secret", Messages: make(chan json.RawMessage, 64), Events: make(chan any, 64)}
	s.HTTP = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gateway" {
			s.gateway(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bot "+s.Token {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v10/users/@me":
			w.Write([]byte(`{"id":"900","username":"Kira"}`))
		case r.URL.Path == "/api/v10/users/@me/guilds":
			w.Write([]byte(`[{"id":"100","name":"Horizon"}]`))
		case strings.Contains(r.URL.Path, "/members/"):
			w.Write([]byte(`{"user":{"id":"1"}}`))
		case strings.HasSuffix(r.URL.Path, "/typing"):
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/messages") && r.Method == "POST":
			var body json.RawMessage
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				w.WriteHeader(400)
				return
			}
			select {
			case s.Messages <- body:
			case <-r.Context().Done():
				return
			}
			w.Write([]byte(`{"id":"999"}`))
		case strings.HasPrefix(r.URL.Path, "/api/v10/channels/"):
			w.Write([]byte(`{"id":"200","name":"general","type":0}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(s.HTTP.Close)
	return s
}
func (s *Server) GatewayURL() string {
	return strings.Replace(s.HTTP.URL, "http:", "ws:", 1) + "/gateway"
}
func (s *Server) gateway(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	write := func(v any) error { data, _ := json.Marshal(v); return conn.Write(ctx, websocket.MessageText, data) }
	if write(map[string]any{"op": 10, "d": map[string]int{"heartbeat_interval": 1000}}) != nil {
		return
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		return
	}
	var auth struct {
		Op   int `json:"op"`
		Data struct {
			Token string `json:"token"`
		} `json:"d"`
	}
	if json.Unmarshal(data, &auth) != nil || auth.Data.Token != s.Token {
		return
	}
	s.mu.Lock()
	s.connections++
	s.mu.Unlock()
	if write(map[string]any{"op": 0, "s": 1, "t": "READY", "d": map[string]string{"session_id": "fake-session", "resume_gateway_url": s.GatewayURL()}}) != nil {
		return
	}
	incoming := make(chan int)
	go func() {
		defer cancel()
		for {
			_, data, e := conn.Read(ctx)
			if e != nil {
				return
			}
			var p struct {
				Op int `json:"op"`
			}
			if json.Unmarshal(data, &p) != nil {
				return
			}
			select {
			case incoming <- p.Op:
			case <-ctx.Done():
				return
			}
		}
	}()
	seq := 2
	for {
		select {
		case <-ctx.Done():
			return
		case op := <-incoming:
			if op == 1 {
				if write(map[string]int{"op": 11}) != nil {
					return
				}
			}
		case event := <-s.Events:
			if write(map[string]any{"op": 0, "s": seq, "t": "MESSAGE_CREATE", "d": event}) != nil {
				return
			}
			seq++
		}
	}
}
