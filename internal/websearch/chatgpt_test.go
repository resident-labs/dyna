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

const testAnswer = "Go is a language and the docs cover it."

// answerStream is the answer stream of one search: text deltas, the finished
// message with its citations, and the completed answer that repeats them.
func answerStream(t *testing.T, answer string, cites ...chatGPTCite) string {
	t.Helper()
	encoded, err := json.Marshal(cites)
	if err != nil {
		t.Fatal(err)
	}
	message := `{"type":"message","content":[{"type":"output_text","text":"` + answer + `","annotations":` + string(encoded) + `}]}`
	start := strings.Index(answer, "the docs")
	annotation := fmt.Sprintf(`data: {"type":"response.output_text.annotation.added","annotation":{"type":"url_citation","url":"https://go.dev/doc","title":"Docs","start_index":%d,"end_index":%d}}`, start, start+len("the docs"))
	return strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"` + answer + `"}`,
		`data: {"type":"response.output_item.done","item":` + message + `}`,
		annotation,
		`data: {"type":"response.completed","response":{"output":[` + message + `]}}`,
		`data: [DONE]`,
	}, "\n\n")
}

func chatGPTServer(t *testing.T, stream string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, stream)
	}))
	t.Cleanup(server.Close)
	return server
}

type chatGPTBody struct {
	Model        string `json:"model"`
	Instructions string `json:"instructions"`
	Input        []struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"input"`
	Tools []struct {
		Type              string `json:"type"`
		ExternalWebAccess bool   `json:"external_web_access"`
	} `json:"tools"`
	Store  bool `json:"store"`
	Stream bool `json:"stream"`
}

func TestChatGPTAsksItsSearchToolAndMapsCitations(t *testing.T) {
	cites := []chatGPTCite{{Type: "url_citation", URL: "https://go.dev", Title: "Go", StartIndex: 0, EndIndex: 16}}
	stream := answerStream(t, testAnswer, cites...)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body chatGPTBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.Method != http.MethodPost || r.URL.Path != "/responses" {
			t.Errorf("request = %s %s, want POST /responses", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer access-token" || r.Header.Get("ChatGPT-Account-ID") != "account-id" || r.Header.Get("originator") != "codex_cli_rs" {
			t.Errorf("headers = %v", r.Header)
		}
		if body.Model != ChatGPTModel || len(body.Tools) != 1 || body.Tools[0].Type != "web_search" || !body.Tools[0].ExternalWebAccess || body.Store || !body.Stream {
			t.Errorf("body = %+v, want the live web search tool, no store, and a stream", body)
		}
		// The backend refuses a search request that carries no instructions.
		if strings.TrimSpace(body.Instructions) == "" {
			t.Errorf("instructions = %q, want the system prompt the backend requires", body.Instructions)
		}
		if len(body.Input) != 1 || len(body.Input[0].Content) != 1 || !strings.Contains(body.Input[0].Content[0].Text, "go language") {
			t.Errorf("input = %+v, want one turn that names the query", body.Input)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, stream)
	}))
	defer server.Close()

	chat := ChatGPT{BaseURL: server.URL, Credential: staticCredential{access: "access-token", account: "account-id"}}
	got, err := chat.Search(t.Context(), Query{Text: "go language", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].URL != "https://go.dev" || got[0].Title != "Go" || got[0].Provider != "chatgpt" || got[1].URL != "https://go.dev/doc" {
		t.Fatalf("results = %+v", got)
	}
	if got[0].Snippet != "Go is a language" {
		t.Fatalf("snippet = %q, want the cited span", got[0].Snippet)
	}
}

// An annotation event adds its source once, and the finished message that
// repeats it does not double the result.
func TestChatGPTReadsCitationsFromTheWholeStream(t *testing.T) {
	server := chatGPTServer(t, answerStream(t, testAnswer, chatGPTCite{Type: "url_citation", URL: "https://go.dev", Title: "Go", StartIndex: 0, EndIndex: 16}))
	chat := ChatGPT{BaseURL: server.URL, Credential: staticCredential{access: "token", account: "account"}}
	got, err := chat.Search(t.Context(), Query{Text: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("results = %+v, want two sources", got)
	}
	if got[0].URL != "https://go.dev" || got[0].Snippet != "Go is a language" {
		t.Fatalf("first result = %+v", got[0])
	}
	if got[1].URL != "https://go.dev/doc" || got[1].Snippet != "the docs" {
		t.Fatalf("second result = %+v, want the annotation event's span", got[1])
	}
}

func TestChatGPTKeepsTheLimit(t *testing.T) {
	server := chatGPTServer(t, answerStream(t, testAnswer, chatGPTCite{Type: "url_citation", URL: "https://go.dev", Title: "Go", StartIndex: 0, EndIndex: 16}))
	chat := ChatGPT{BaseURL: server.URL, Credential: staticCredential{access: "token", account: "account"}}
	got, err := chat.Search(t.Context(), Query{Text: "go", Limit: 1})
	if err != nil || len(got) != 1 || got[0].URL != "https://go.dev" {
		t.Fatalf("results = %+v, %v; want only the first source", got, err)
	}
}

func TestChatGPTWithoutASignInIsNotSignedIn(t *testing.T) {
	chat := ChatGPT{Credential: staticCredential{err: ErrNotSignedIn}}
	if _, err := chat.Search(t.Context(), Query{Text: "go"}); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("err = %v, want ErrNotSignedIn", err)
	}
	if _, err := (ChatGPT{}).Search(t.Context(), Query{Text: "go"}); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("without a credential: err = %v, want ErrNotSignedIn", err)
	}
}

// A stream error is a failure, never an empty answer with no source.
func TestChatGPTReportsAStreamError(t *testing.T) {
	server := chatGPTServer(t, "data: {\"type\":\"error\",\"message\":\"rate limit\"}\n\ndata: [DONE]\n\n")
	chat := ChatGPT{BaseURL: server.URL, Credential: staticCredential{access: "token", account: "account"}}
	_, err := chat.Search(t.Context(), Query{Text: "go"})
	if err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("err = %v, want the stream's reason", err)
	}
}

// staticCredential is one ChatGPT sign-in for a test.
type staticCredential struct {
	access, account string
	err             error
}

func (c staticCredential) Token(context.Context) (string, string, error) {
	return c.access, c.account, c.err
}
