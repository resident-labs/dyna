// Package fakediscord serves a token-checking Discord REST and gateway fixture.
package fakediscord

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type Server struct {
	HTTP          *httptest.Server
	Token         string
	Messages      chan json.RawMessage
	UploadedFiles chan string
	Events        chan any
	mu            sync.Mutex
	connections   int
	history       []any
	historyCalls  int
	requests      int
	configuration *Configuration
	requestLog    []Request
	nextID        int
	writes        int
	failure       writeFailure
}

func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{Token: "fake-discord-secret", Messages: make(chan json.RawMessage, 64), UploadedFiles: make(chan string, 64), Events: make(chan any, 64)}
	s.HTTP = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil {
			var err error
			body, err = io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(400)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		s.mu.Lock()
		s.requests++
		s.requestLog = append(s.requestLog, Request{Method: r.Method, Path: r.URL.RequestURI(), Body: string(body)})
		s.mu.Unlock()
		if r.URL.Path == "/gateway" {
			s.gateway(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bot "+s.Token {
			w.WriteHeader(401)
			return
		}
		if status, applyFirst := s.injectedFailure(r.Method); status != 0 {
			if applyFirst {
				s.serve(httptest.NewRecorder(), r, body)
			}
			w.WriteHeader(status)
			return
		}
		s.serve(w, r, body)
	}))
	t.Cleanup(s.HTTP.Close)
	return s
}

// serve answers one authenticated REST request.
func (s *Server) serve(w http.ResponseWriter, r *http.Request, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	if s.configurationGET(w, r) || s.configurationWrite(w, r, body) {
		return
	}
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
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			if err := r.ParseMultipartForm(10 << 20); err != nil {
				w.WriteHeader(400)
				return
			}
			defer r.MultipartForm.RemoveAll()
			body = json.RawMessage(r.FormValue("payload_json"))
			for _, files := range r.MultipartForm.File {
				for _, file := range files {
					s.UploadedFiles <- file.Filename
				}
			}
		} else if json.NewDecoder(r.Body).Decode(&body) != nil {
			w.WriteHeader(400)
			return
		}
		select {
		case s.Messages <- body:
		case <-r.Context().Done():
			return
		}
		w.Write([]byte(`{"id":"` + s.newID() + `"}`))
	case strings.Contains(r.URL.Path, "/messages/pins/"):
		w.WriteHeader(204)
	case strings.Contains(r.URL.Path, "/messages/") && r.Method == "PATCH":
		w.Write([]byte(`{"id":"` + r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:] + `"}`))
	case strings.HasSuffix(r.URL.Path, "/messages") && r.Method == "GET":
		s.mu.Lock()
		s.historyCalls++
		history := s.history
		s.mu.Unlock()
		json.NewEncoder(w).Encode(history)
	case strings.Contains(r.URL.Path, "/messages/") && r.Method == "GET":
		s.mu.Lock()
		history := s.history
		s.mu.Unlock()
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		for _, message := range history {
			data, _ := json.Marshal(message)
			var identity struct {
				ID string `json:"id"`
			}
			json.Unmarshal(data, &identity)
			if identity.ID == id {
				w.Write(data)
				return
			}
		}
		w.WriteHeader(404)
	case strings.HasPrefix(r.URL.Path, "/api/v10/guilds/"):
		w.Write([]byte(`{"id":"100","name":"Horizon"}`))
	case strings.HasPrefix(r.URL.Path, "/api/v10/channels/"):
		w.Write([]byte(`{"id":"200","guild_id":"100","name":"general","type":0}`))
	default:
		w.WriteHeader(404)
	}
}
func (s *Server) SetHistory(messages []any) { s.mu.Lock(); defer s.mu.Unlock(); s.history = messages }
func (s *Server) Requests() int             { s.mu.Lock(); defer s.mu.Unlock(); return s.requests }
func (s *Server) Connections() int          { s.mu.Lock(); defer s.mu.Unlock(); return s.connections }
func (s *Server) HistoryCalls() int         { s.mu.Lock(); defer s.mu.Unlock(); return s.historyCalls }
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
	if write(map[string]any{"op": 0, "s": 2, "t": "GUILD_CREATE", "d": map[string]any{"id": "100", "name": "Horizon", "channels": []map[string]any{{"id": "200", "name": "general", "type": 0}}, "roles": []map[string]any{{"id": "10", "name": "Admin"}, {"id": "11", "name": "bot", "tags": map[string]string{"bot_id": "900"}}}}}) != nil {
		return
	}
	seq := 3
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
