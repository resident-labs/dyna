package cliproxy

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type searchTransport struct{ target string }

func (t searchTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.URL.Scheme = "http"
	r.URL.Host = t.target
	return http.DefaultTransport.RoundTrip(r)
}

func TestDynaSearchZai(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer zai-secret" {
			t.Error("wrong upstream credential")
		}
		if calls == 1 {
			w.Header().Set("Mcp-Session-Id", "session")
			fmt.Fprint(w, "{}")
			return
		}
		if r.Header.Get("Mcp-Session-Id") != "session" {
			t.Error("missing session")
		}
		fmt.Fprint(w, `{"result":{"content":[{"type":"text","text":"[{\"title\":\"A\",\"link\":\"https://example.test\",\"content\":\"B\"}]"}]}}`)
	}))
	defer server.Close()
	old := zaiClient
	zaiClient = &http.Client{Transport: searchTransport{strings.TrimPrefix(server.URL, "http://")}}
	defer func() { zaiClient = old }()
	manager := coreauth.NewManager(nil, nil, nil)
	_, err := manager.Register(context.Background(), &coreauth.Auth{ID: "z", Provider: "openai-compatible-zai", Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "zai-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{coreManager: manager}
	results, err := service.DynaSearch(context.Background(), "zai", "query", 1, []string{base64.RawURLEncoding.EncodeToString([]byte("z"))})
	if err != nil || len(results) != 1 || results[0].Title != "A" || calls != 2 {
		t.Fatalf("unexpected results: %#v, %v, %d calls", results, err, calls)
	}
}

func TestSearchRefusalSanitizesTokensAndDropsRawBody(t *testing.T) {
	body := `{"message":"Bearer abcdefghijk sk-abcdefghijk eyJabcdefghijklmnop"}`
	err := statusError("kimi", http.StatusForbidden, strings.NewReader(body))
	if got := err.Error(); got != "kimi answered HTTP 403: [redacted] [redacted] [redacted]" {
		t.Fatalf("unexpected sanitized refusal: %q", got)
	}
	err = statusError("kimi", http.StatusForbidden, strings.NewReader("private upstream body"))
	if got := err.Error(); got != "kimi answered HTTP 403" {
		t.Fatalf("raw body leaked: %q", got)
	}
}

func TestDynaSearchRefusal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"message":"Plan unavailable for secret-value.\nTry another plan."}}`)
	}))
	defer server.Close()
	old := kimiClient
	kimiClient = &http.Client{Transport: searchTransport{strings.TrimPrefix(server.URL, "http://")}}
	defer func() { kimiClient = old }()
	manager := coreauth.NewManager(nil, nil, nil)
	// SDK credential metadata has an open provider-specific shape.
	_, err := manager.Register(context.Background(), &coreauth.Auth{ID: "allowed", Provider: "kimi", Status: coreauth.StatusActive, Metadata: map[string]any{"access_token": "secret-value"}})
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{coreManager: manager}
	_, err = service.DynaSearch(context.Background(), "kimi", "query", 1, []string{base64.RawURLEncoding.EncodeToString([]byte("allowed"))})
	if err == nil || err.Error() != "kimi answered HTTP 403: Plan unavailable for [redacted]. Try another plan." {
		t.Fatalf("unexpected refusal: %v", err)
	}
	refusal, ok := err.(searchRefusal)
	if !ok || refusal.StatusCode() != http.StatusForbidden {
		t.Fatal("lost refusal status")
	}
}

func TestDynaSearchEligibilityAndRedaction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-value" {
			t.Error("wrong upstream credential")
		}
		fmt.Fprint(w, `{"search_results":[{"title":"secret-value","url":"https://example.test","snippet":"found"}]}`)
	}))
	defer server.Close()
	old := kimiClient
	kimiClient = &http.Client{Transport: searchTransport{strings.TrimPrefix(server.URL, "http://")}}
	defer func() { kimiClient = old }()
	manager := coreauth.NewManager(nil, nil, nil)
	// SDK credential metadata has an open provider-specific shape.
	_, err := manager.Register(context.Background(), &coreauth.Auth{ID: "allowed", Provider: "kimi", Status: coreauth.StatusActive, Metadata: map[string]any{"access_token": "secret-value"}})
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{coreManager: manager}
	encode := func(id string) []string { return []string{base64.RawURLEncoding.EncodeToString([]byte(id))} }
	if _, err = service.DynaSearch(context.Background(), "kimi", "query", 1, encode("other")); err == nil {
		t.Fatal("unlisted credential accepted")
	}
	results, err := service.DynaSearch(context.Background(), "kimi", "query", 1, encode("allowed"))
	if err != nil || len(results) != 1 || results[0].Title != "[redacted]" {
		t.Fatalf("unexpected search results: %#v, %v", results, err)
	}
}
