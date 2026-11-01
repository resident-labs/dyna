package websearch

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type searchTransport func(*http.Request) (*http.Response, error)

func (f searchTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestModelSearchProxyToolsAndSafeRefusal(t *testing.T) {
	for _, name := range []string{"chatgpt", "xai", "claude"} {
		t.Run(name, func(t *testing.T) {
			client := &http.Client{Transport: searchTransport(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Authorization") != "" {
					t.Error("provider sent model credentials")
				}
				var body struct {
					Tools []struct {
						Type string `json:"type"`
					} `json:"tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				want := "web_search"
				if name == "claude" {
					want = "web_search_20250305"
				}
				if len(body.Tools) != 1 || body.Tools[0].Type != want {
					t.Fatalf("tools = %+v", body.Tools)
				}
				return &http.Response{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"bad Bearer secret-token-value"}}`)), Request: r}, nil
			})}
			var provider Provider
			switch name {
			case "chatgpt":
				provider = ChatGPT{BaseURL: "http://cliproxy/v1", Client: client}
			case "xai":
				provider = XAI{BaseURL: "http://cliproxy/v1", Client: client}
			case "claude":
				provider = Claude{BaseURL: "http://cliproxy/v1", Client: client}
			}
			_, err := provider.Search(t.Context(), Query{Text: "test"})
			if err == nil || strings.Contains(err.Error(), "secret-token-value") || !strings.Contains(err.Error(), "[redacted]") {
				t.Fatalf("unsafe refusal: %v", err)
			}
		})
	}
}
