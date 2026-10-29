package roommcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientRefusesCrossOriginRedirect(t *testing.T) {
	reached := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	c, err := NewClient(server.URL, "fixture-secret")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.request(context.Background(), "tools/list", nil)
	if err == nil || reached {
		t.Fatalf("redirect: err=%v reached=%v", err, reached)
	}
	if strings.Contains(err.Error(), "fixture-secret") {
		t.Fatal("secret in error")
	}
}

func TestClientRequestIdentityAndSession(t *testing.T) {
	n := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if r.Header.Get("Authorization") != "Bearer fixture-secret" {
			t.Error("missing authorization")
		}
		if r.Header.Get("User-Agent") != "dyna" {
			t.Error("unexpected user agent")
		}
		if n == 2 && r.Header.Get("Mcp-Session-Id") != "session" {
			t.Error("missing session")
		}
		var request struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			ID     int             `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "session")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer server.Close()
	c, err := NewClient(server.URL, "fixture-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.request(context.Background(), "initialize", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.request(context.Background(), "tools/list", nil); err != nil {
		t.Fatal(err)
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Unix(1000, 0)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer server.Close()
	c, err := NewClient(server.URL, "fixture-secret")
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return now }
	if _, err = c.request(context.Background(), "tools/list", nil); err == nil {
		t.Fatal("expected 429")
	}
	if _, err = c.request(context.Background(), "tools/list", nil); err == nil || requests != 1 {
		t.Fatal("expected local refusal")
	}
	now = now.Add(30 * time.Second)
	if _, err = c.request(context.Background(), "tools/list", nil); err != nil || requests != 2 {
		t.Fatalf("retry: %v", err)
	}
}

func TestRetryAfterIsCapped(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "2000000000")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	c, err := NewClient(server.URL, "fixture-secret")
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return now }
	_, _ = c.request(context.Background(), "tools/list", nil)
	if want := now.Add(maxRetryAfter); !c.retryUntil.Equal(want) {
		t.Fatalf("hold until %v, want %v", c.retryUntil, want)
	}
}

func TestClientRejectsNonLoopbackHTTP(t *testing.T) {
	if _, err := NewClient("http://example.com/mcp", "fixture-secret"); err == nil {
		t.Fatal("accepted HTTP")
	}
}
