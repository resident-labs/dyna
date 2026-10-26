package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// claudeEvents joins the server-sent events of one answer.
func claudeStream(events ...string) string {
	return strings.Join(events, "\n\n") + "\n\n"
}

// claudeEvent is one server-sent event with its name and data.
func claudeSSEEvent(name, data string) string {
	return "event: " + name + "\ndata: " + data
}

// claudeServer answers every request with one stream and records its header and
// body, so a test can check the request the provider sends.
func claudeServer(t *testing.T, stream string, seen func(*http.Request)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			seen(r)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, stream)
	}))
	t.Cleanup(server.Close)
	return server
}

// claudeBody is the request body a test reads.
type claudeBody struct {
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
	System    []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"system"`
	Messages []struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		MaxUses int    `json:"max_uses"`
	} `json:"tools"`
	ToolChoice struct {
		Type string `json:"type"`
		Name string `json:"name"`
	} `json:"tool_choice"`
	Stream bool `json:"stream"`
}

// tokenFunc is one saved Claude sign-in for a test.
type tokenFunc func(context.Context) (string, error)

const claudeSearchAnswer = `{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{}}}`

func TestClaudeForcesItsSearchToolAndMapsItsHits(t *testing.T) {
	stream := claudeStream(
		claudeSSEEvent("message_start", `{"type":"message_start","message":{"id":"msg_1","role":"assistant"}}`),
		claudeSSEEvent("content_block_start", claudeSearchAnswer),
		claudeSSEEvent("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[`+
			`{"type":"web_search_result","title":"Go","url":"https://go.dev"},`+
			`{"type":"web_search_result","title":"Docs","url":"https://go.dev/doc"},`+
			`{"type":"web_search_result","title":"","url":"https://go.dev/pkg"},`+
			`{"type":"web_search_result","title":"Go again","url":"https://go.dev"}]}}`),
		claudeSSEEvent("content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`),
		claudeSSEEvent("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"citations_delta","citation":{"type":"web_search_result_location","url":"https://go.dev","title":"Go","cited_text":"Go is a language."}}}`),
		claudeSSEEvent("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"citations_delta","citation":{"type":"web_search_result_location","url":"https://go.dev/pkg","title":"Packages","cited_text":"The package index."}}}`),
		claudeSSEEvent("message_stop", `{"type":"message_stop"}`),
	)
	var body claudeBody
	server := claudeServer(t, stream, func(r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" || r.URL.RawQuery != "beta=true" {
			t.Errorf("request = %s %s?%s, want POST /v1/messages?beta=true", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token-1" {
			t.Errorf("Authorization = %q, want the saved token", got)
		}
		if got := r.Header.Get("anthropic-version"); got != claudeVersion {
			t.Errorf("anthropic-version = %q", got)
		}
		beta := r.Header.Get("anthropic-beta")
		for _, want := range []string{"claude-code-20250219", "oauth-2025-04-20", "web-search-2025-03-05"} {
			if !strings.Contains(beta, want) {
				t.Errorf("anthropic-beta = %q, missing %q", beta, want)
			}
		}
		if r.Header.Get("x-app") != claudeApp || !strings.HasPrefix(r.Header.Get("User-Agent"), "claude-cli/") {
			t.Errorf("client headers = %v", r.Header)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
	})
	defer server.Close()

	claude := Claude{BaseURL: server.URL, Token: tokenFunc(func(context.Context) (string, error) { return "token-1", nil })}
	got, err := claude.Search(t.Context(), Query{Text: "go language", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if body.Model != ClaudeModel || !body.Stream || body.MaxTokens == 0 {
		t.Errorf("body = %+v, want the model, a stream, and a token cap", body)
	}
	if len(body.Tools) != 1 || body.Tools[0].Type != claudeSearchTool || body.Tools[0].Name != claudeSearchName || body.Tools[0].MaxUses != claudeMaxUses {
		t.Errorf("tools = %+v, want only the search tool", body.Tools)
	}
	if body.ToolChoice.Type != "tool" || body.ToolChoice.Name != claudeSearchName {
		t.Errorf("tool_choice = %+v, want the forced search tool", body.ToolChoice)
	}
	if len(body.System) != 2 || body.System[0].Text != claudeIdentity || body.System[1].Text != claudeInstructions {
		t.Errorf("system = %+v, want the client identity first and then the search instruction", body.System)
	}
	if len(body.Messages) != 1 || len(body.Messages[0].Content) != 1 || !strings.Contains(body.Messages[0].Content[0].Text, "go language") {
		t.Errorf("messages = %+v, want one turn that names the query", body.Messages)
	}
	if len(got) != 3 {
		t.Fatalf("results = %+v, want three pages", got)
	}
	if got[0].Title != "Go" || got[0].URL != "https://go.dev" || got[0].Provider != "claude" || got[0].Snippet != "Go is a language." {
		t.Fatalf("first result = %+v, want the hit with its cited sentence", got[0])
	}
	if got[1].URL != "https://go.dev/doc" || got[1].Title != "Docs" {
		t.Fatalf("second result = %+v, want the second page only once", got[1])
	}
	if got[2].URL != "https://go.dev/pkg" || got[2].Title != "Packages" || got[2].Snippet != "The package index." {
		t.Fatalf("third result = %+v, want the citation's own title and sentence", got[2])
	}
}

func TestClaudeWithoutASignInIsNotSignedIn(t *testing.T) {
	if _, err := (Claude{}).Search(t.Context(), Query{Text: "go"}); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("without a token reader: err = %v, want ErrNotSignedIn", err)
	}
	for _, token := range []tokenFunc{
		func(context.Context) (string, error) { return "", nil },
		func(context.Context) (string, error) { return "", ErrNotSignedIn },
	} {
		claude := Claude{Token: token}
		if _, err := claude.Search(t.Context(), Query{Text: "go"}); !errors.Is(err, ErrNotSignedIn) {
			t.Fatalf("empty or refused token: err = %v, want ErrNotSignedIn", err)
		}
	}
}

// A rejected token makes the chain skip Claude instead of failing the search.
func TestClaudeTreatsARejectedTokenAsNotSignedIn(t *testing.T) {
	var asked bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked = true
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"type":"error","error":{"type":"authentication_error"}}`)
	}))
	defer server.Close()
	claude := Claude{BaseURL: server.URL, Token: tokenFunc(func(context.Context) (string, error) { return "token-9", nil })}
	_, err := claude.Search(t.Context(), Query{Text: "go"})
	if !errors.Is(err, ErrNotSignedIn) || !asked {
		t.Fatalf("err = %v after %v, want ErrNotSignedIn", err, asked)
	}
	if strings.Contains(err.Error(), "token-9") {
		t.Fatalf("error names the token: %q", err)
	}
}

// A stream error is the one reason reported, and the rest of the answer never
// reaches the message.
func TestClaudeReportsAStreamErrorWithoutItsBody(t *testing.T) {
	stream := claudeStream(
		claudeSSEEvent("message_start", `{"type":"message_start","message":{"id":"msg_1","note":"sk-ant-api03-secret"}}`),
		claudeSSEEvent("error", `{"type":"error","error":{"type":"rate_limit_error","message":"credit balance is too low"}}`),
	)
	server := claudeServer(t, stream, nil)
	claude := Claude{BaseURL: server.URL, Token: tokenFunc(func(context.Context) (string, error) { return "token-2", nil })}
	_, err := claude.Search(t.Context(), Query{Text: "go"})
	if err == nil || !strings.Contains(err.Error(), "credit balance is too low") {
		t.Fatalf("err = %v, want the stream's reason", err)
	}
	if strings.Contains(err.Error(), "sk-ant") || strings.Contains(err.Error(), "\n") {
		t.Fatalf("error leaks or splits the answer: %q", err)
	}
}

// A search the API could not run names its error code and nothing else.
func TestClaudeReportsASearchErrorCode(t *testing.T) {
	stream := claudeStream(
		claudeSSEEvent("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"web_search_tool_result_error","error_code":"max_uses_exceeded"}}}`),
	)
	server := claudeServer(t, stream, nil)
	claude := Claude{BaseURL: server.URL, Token: tokenFunc(func(context.Context) (string, error) { return "token-3", nil })}
	_, err := claude.Search(t.Context(), Query{Text: "go"})
	if err == nil || !strings.Contains(err.Error(), "max_uses_exceeded") {
		t.Fatalf("err = %v, want the search's error code", err)
	}
}

// A search that returns no page is a failure, so the chain tries the next
// provider instead of reporting an empty answer.
func TestClaudeReportsASearchWithoutHits(t *testing.T) {
	stream := claudeStream(
		claudeSSEEvent("message_start", `{"type":"message_start","message":{"id":"msg_1"}}`),
		claudeSSEEvent("message_stop", `{"type":"message_stop"}`),
	)
	server := claudeServer(t, stream, nil)
	claude := Claude{BaseURL: server.URL, Token: tokenFunc(func(context.Context) (string, error) { return "token-4", nil })}
	_, err := claude.Search(t.Context(), Query{Text: "go"})
	if err == nil || !strings.Contains(err.Error(), "no search results") {
		t.Fatalf("err = %v, want a no-results failure", err)
	}
}
