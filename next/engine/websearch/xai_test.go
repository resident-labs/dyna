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

const xaiText = "Go is a language and it has docs.[[1]](https://go.dev/doc) The release notes list the changes."

// xaiCite is the citation the answer carries in the shape xAI documents: the
// marker sits after the sentence's terminator, and the title is only its label.
func xaiCite() xaiCitation {
	start := strings.Index(xaiText, "[[1]]")
	return xaiCitation{Type: "url_citation", URL: "https://go.dev/doc", Title: "1", StartIndex: start, EndIndex: start + len("[[1]](https://go.dev/doc)")}
}

// xaiStreamBody is one answer stream: the text, the citation, and the finished
// search call that repeats its sources.
func xaiStreamBody(t *testing.T, answer string, cite xaiCitation, sources ...xaiSource) string {
	t.Helper()
	annotation, err := json.Marshal(cite)
	if err != nil {
		t.Fatal(err)
	}
	call, err := json.Marshal(xaiItem{Type: "web_search_call", Action: &xaiSearchCall{Sources: sources}})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"` + answer + `"}`,
		`data: {"type":"response.output_text.annotation.added","annotation":` + string(annotation) + `}`,
		`data: {"type":"response.output_item.done","item":` + string(call) + `}`,
		`data: {"type":"response.completed","response":{"output":[` + string(call) + `]}}`,
		`data: [DONE]`,
		"",
	}, "\n\n")
}

// xaiServer answers every search with one stream.
func xaiServer(t *testing.T, stream string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, stream)
	}))
	t.Cleanup(server.Close)
	return server
}

type xaiBody struct {
	Model string `json:"model"`
	Input []struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"input"`
	Tools []struct {
		Type string `json:"type"`
	} `json:"tools"`
	Include []string `json:"include"`
	Store   bool     `json:"store"`
	Stream  bool     `json:"stream"`
}

func TestXAIAsksItsSearchToolAndMapsCitations(t *testing.T) {
	stream := xaiStreamBody(t, xaiText, xaiCite(), xaiSource{URL: "https://go.dev", Title: "Go"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/responses" {
			t.Errorf("request = %s %s, want POST /responses", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer session-token" {
			t.Errorf("Authorization = %q, want the saved sign-in", got)
		}
		if r.Header.Get("X-Xai-Token-Auth") != xaiTokenAuth || r.Header.Get("X-Grok-Model-Override") != XAIModel {
			t.Errorf("headers = %v, want the Grok CLI sign-in and model override", r.Header)
		}
		if r.Header.Get("X-Grok-Client-Identifier") != xaiClientIdentifier || r.Header.Get("X-Grok-Client-Mode") != xaiClientMode || r.Header.Get("X-Grok-Client-Version") != xaiClientVersion {
			t.Errorf("headers = %v, want the Grok Build client name", r.Header)
		}
		var body xaiBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != XAIModel || len(body.Tools) != 1 || body.Tools[0].Type != "web_search" || body.Store || !body.Stream {
			t.Errorf("body = %+v, want the web search tool, no store, and a stream", body)
		}
		if len(body.Include) != 1 || body.Include[0] != "web_search_call.action.sources" {
			t.Errorf("include = %v, want the search call sources", body.Include)
		}
		if len(body.Input) != 1 || len(body.Input[0].Content) != 1 || !strings.Contains(body.Input[0].Content[0].Text, "go language") {
			t.Errorf("input = %+v, want one turn that names the query", body.Input)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, stream)
	}))
	defer server.Close()

	search := XAI{BaseURL: server.URL, Token: xaiToken("session-token")}
	got, err := search.Search(t.Context(), Query{Text: "go language", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("results = %+v, want the citation and then its source", got)
	}
	if got[0].URL != "https://go.dev/doc" || got[0].Title != "https://go.dev/doc" || got[0].Provider != "xai" {
		t.Fatalf("first result = %+v, want the cited source named by its URL: the label \"1\" names nothing", got[0])
	}
	if got[0].Snippet != "Go is a language and it has docs." {
		t.Fatalf("snippet = %q, want the sentence before the citation, and not the one after it", got[0].Snippet)
	}
	if got[1].URL != "https://go.dev" || got[1].Title != "Go" {
		t.Fatalf("second result = %+v, want the search call source", got[1])
	}
	if got[1].Snippet != "" {
		t.Fatalf("snippet = %q, want none: a source carries no snippet", got[1].Snippet)
	}
}

// Adjacent citations each name the sentence before them, and a URL inside a
// neighbouring marker never looks like a sentence end.
func TestXAISnippetsTheProseBeforeAdjacentCitations(t *testing.T) {
	const answer = "Grok is an AI assistant.[[1]](https://x.ai/)[[2]](https://example.com/a.b)"
	first := xaiCitation{Type: "url_citation", URL: "https://x.ai/", Title: "1", StartIndex: strings.Index(answer, "[[1]]")}
	first.EndIndex = first.StartIndex + len("[[1]](https://x.ai/)")
	second := xaiCitation{Type: "url_citation", URL: "https://example.com/a.b", Title: "2", StartIndex: strings.Index(answer, "[[2]]")}
	second.EndIndex = second.StartIndex + len("[[2]](https://example.com/a.b)")
	server := xaiServer(t, xaiTwoCitationStream(t, answer, first, second))
	got, err := (XAI{BaseURL: server.URL, Token: xaiToken("token")}).Search(t.Context(), Query{Text: "grok"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("results = %+v, want both citations", got)
	}
	for i, result := range got {
		if result.Snippet != "Grok is an AI assistant." {
			t.Fatalf("result %d snippet = %q, want the cited sentence", i, result.Snippet)
		}
	}
}

// xaiTwoCitationStream is one answer stream that carries two citations, in the
// adjacent shape xAI writes when a sentence has more than one source.
func xaiTwoCitationStream(t *testing.T, answer string, cites ...xaiCitation) string {
	t.Helper()
	lines := []string{`data: {"type":"response.output_text.delta","delta":"` + answer + `"}`}
	for _, cite := range cites {
		encoded, err := json.Marshal(cite)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, `data: {"type":"response.output_text.annotation.added","annotation":`+string(encoded)+`}`)
	}
	return strings.Join(append(lines, `data: [DONE]`, ""), "\n\n")
}

// A source the answer cited twice, or that the search call repeats, is one
// result.
func TestXAIKeepsEachSourceOnce(t *testing.T) {
	cite := xaiCite()
	stream := xaiStreamBody(t, xaiText, cite, xaiSource{URL: cite.URL, Title: "Docs"}, xaiSource{URL: "https://go.dev", Title: "Go"})
	for _, test := range []struct {
		name  string
		limit int
		want  int
	}{
		{name: "all", want: 2},
		{name: "capped", limit: 1, want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := xaiServer(t, stream)
			search := XAI{BaseURL: server.URL, Token: xaiToken("token")}
			got, err := search.Search(t.Context(), Query{Text: "go", Limit: test.limit})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != test.want || got[0].URL != "https://go.dev/doc" {
				t.Fatalf("results = %+v, want %d result(s) with the citation first", got, test.want)
			}
		})
	}
}

func TestXAIWithoutASignInIsNotSignedIn(t *testing.T) {
	server := xaiServer(t, xaiStreamBody(t, xaiText, xaiCite()))
	if _, err := (XAI{BaseURL: server.URL}).Search(t.Context(), Query{Text: "go"}); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("without a credential: err = %v, want ErrNotSignedIn", err)
	}
	if _, err := (XAI{BaseURL: server.URL, Token: xaiToken("")}).Search(t.Context(), Query{Text: "go"}); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("with an empty token: err = %v, want ErrNotSignedIn", err)
	}
	missing := XAI{BaseURL: server.URL, Token: func(context.Context) (string, error) { return "", ErrNotSignedIn }}
	if _, err := missing.Search(t.Context(), Query{Text: "go"}); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("without a saved sign-in: err = %v, want ErrNotSignedIn", err)
	}
}

// A refusal names the status and the reason the provider gave, and never the
// token, which the reason can quote back.
func TestXAIRefusalLeaksNoToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"error":{"message":"invalid token: Bearer sekret-token for go.dev"}}`)
	}))
	defer server.Close()

	search := XAI{BaseURL: server.URL, Token: xaiToken("sekret-token")}
	_, err := search.Search(t.Context(), Query{Text: "go"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || !strings.Contains(err.Error(), "invalid token") {
		t.Fatalf("err = %v, want the status and the reason", err)
	}
	if strings.Contains(err.Error(), "sekret-token") {
		t.Fatalf("err = %q, want no token", err)
	}
}

// A stream error is a failure, never an empty answer with no source.
func TestXAIReportsAStreamError(t *testing.T) {
	server := xaiServer(t, "data: {\"type\":\"error\",\"message\":\"rate limit\"}\n\ndata: [DONE]\n\n")
	search := XAI{BaseURL: server.URL, Token: xaiToken("token")}
	_, err := search.Search(t.Context(), Query{Text: "go"})
	if err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("err = %v, want the stream's reason", err)
	}
}

// xaiToken is one Grok sign-in for a test.
func xaiToken(token string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return token, nil }
}
