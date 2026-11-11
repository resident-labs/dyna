package discord

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRESTBoundary(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bot secret" {
			t.Error("missing authentication")
		}
		w.Write([]byte(`{"value":"secret"}`))
	}))
	defer server.Close()
	c := Client{HTTP: server.Client(), Endpoint: server.URL, Token: "secret"}
	for _, path := range []string{"https://evil.example/api/v10/channels", "//evil.example/api/v10/channels", "/api/v10/../users", "/api/v10/users/@me", "/api/v10/users/@me/avatar", "/other", "/api/v10/%2e%2e/channels"} {
		if _, err := c.API(context.Background(), "POST", path, nil, ""); err == nil {
			t.Errorf("accepted %s", path)
		}
	}
	if calls != 0 {
		t.Fatal("refused paths reached transport")
	}
	result, err := c.API(context.Background(), "GET", "/api/v10/users/@me", nil, "")
	if err != nil || strings.Contains(string(result.Body), "secret") {
		t.Fatalf("credential reflected: %v", err)
	}
}
func TestRateLimitAndTruncation(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(429)
			w.Write([]byte(`{"retry_after":0.001}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"text": strings.Repeat("x", 30000)})
	}))
	defer server.Close()
	c := Client{HTTP: server.Client(), Endpoint: server.URL, Token: "secret"}
	r, err := c.API(context.Background(), "GET", "/api/v10/channels/1", nil, "")
	if err != nil || calls != 2 || !json.Valid(r.Body) || !strings.Contains(string(r.Body), "truncated") {
		t.Fatalf("retry/truncate: %d %v", calls, err)
	}
}
