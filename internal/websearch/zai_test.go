package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// zaiRequest is one request the fake MCP server received.
type zaiRequest struct {
	HTTP    string
	Path    string
	Auth    string
	Accept  string
	Session string
	Body    struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  struct {
			ProtocolVersion string `json:"protocolVersion"`
			Name            string `json:"name"`
			Arguments       struct {
				SearchQuery string `json:"search_query"`
			} `json:"arguments"`
		} `json:"params"`
	}
}

const zaiTestPayload = `{"results":[` +
	`{"title":"Go","link":"https://go.dev","content":"The Go language.","refer":"go.dev"},` +
	`{"title":"Docs","link":"https://go.dev/doc","content":"Documentation.","refer":"go.dev"}]}`

// zaiServer answers the initialize call and then the search call. The search
// answer carries text, quoted once more when double is set.
func zaiServer(t *testing.T, text string) (*httptest.Server, *[]zaiRequest) {
	t.Helper()
	calls := &[]zaiRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got zaiRequest
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if err := json.Unmarshal(raw, &got.Body); err != nil {
			t.Errorf("body %q: %v", raw, err)
		}
		got.HTTP, got.Path = r.Method, r.URL.Path
		got.Auth, got.Accept, got.Session = r.Header.Get("Authorization"), r.Header.Get("Accept"), r.Header.Get("Mcp-Session-Id")
		*calls = append(*calls, got)
		w.Header().Set("Content-Type", "text/event-stream")
		if len(*calls) == 1 {
			if got.Body.Method != "initialize" {
				t.Errorf("first call = %q, want the initialize call", got.Body.Method)
			}
			w.Header().Set("Mcp-Session-Id", "session-1")
			_, _ = io.WriteString(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":\"2025-03-26\"}}\n\n")
			return
		}
		_, _ = fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[{\"type\":\"text\",\"text\":%q}]}}\n\n", text)
	}))
	t.Cleanup(server.Close)
	return server, calls
}

func TestZaiOpensASessionThenCallsItsSearchTool(t *testing.T) {
	server, calls := zaiServer(t, zaiTestPayload)
	zai := Zai{BaseURL: server.URL + "/mcp", Key: func(context.Context) (string, error) { return "zai-key", nil }}

	got, err := zai.Search(t.Context(), Query{Text: "go language"})
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 {
		t.Fatalf("calls = %d, want the initialize call and one tools/call", len(*calls))
	}
	first, second := (*calls)[0], (*calls)[1]
	if first.HTTP != http.MethodPost || first.Path != "/mcp" || first.Auth != "Bearer zai-key" {
		t.Fatalf("initialize request = %s %s auth %q", first.HTTP, first.Path, first.Auth)
	}
	if !strings.Contains(first.Accept, "application/json") || !strings.Contains(first.Accept, "text/event-stream") {
		t.Fatalf("Accept = %q, want JSON and a stream", first.Accept)
	}
	if first.Body.JSONRPC != "2.0" || first.Body.Params.ProtocolVersion != zaiProtocol || first.Session != "" {
		t.Fatalf("initialize body = %+v, session header %q", first.Body, first.Session)
	}
	if second.Body.Method != "tools/call" || second.Body.Params.Name != zaiTool || second.Body.Params.Arguments.SearchQuery != "go language" {
		t.Fatalf("search body = %+v, want tools/call of %s", second.Body, zaiTool)
	}
	if second.Session != "session-1" || second.Auth != "Bearer zai-key" {
		t.Fatalf("search session = %q auth = %q", second.Session, second.Auth)
	}
	if len(got) != 2 {
		t.Fatalf("results = %+v, want two", got)
	}
	want := Result{Title: "Go", URL: "https://go.dev", Snippet: "The Go language.", Provider: "zai"}
	if got[0] != want {
		t.Fatalf("first result = %+v, want %+v", got[0], want)
	}
	if got[1].URL != "https://go.dev/doc" || got[1].Title != "Docs" {
		t.Fatalf("second result = %+v", got[1])
	}
}

// The tool quotes its JSON payload one extra time on some answers.
func TestZaiReadsADoubleEncodedAnswer(t *testing.T) {
	server, _ := zaiServer(t, `"`+strings.ReplaceAll(zaiTestPayload, `"`, `\"`)+`"`)
	zai := Zai{BaseURL: server.URL + "/mcp", Key: func(context.Context) (string, error) { return "zai-key", nil }}

	got, err := zai.Search(t.Context(), Query{Text: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].URL != "https://go.dev" || got[0].Snippet != "The Go language." {
		t.Fatalf("results = %+v, want the quoted payload read once more", got)
	}
}

func TestZaiWithoutAKeyIsNotSignedIn(t *testing.T) {
	if _, err := (Zai{}).Search(t.Context(), Query{Text: "go"}); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("without a key reader: err = %v, want ErrNotSignedIn", err)
	}
	empty := Zai{Key: func(context.Context) (string, error) { return "", nil }}
	if _, err := empty.Search(t.Context(), Query{Text: "go"}); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("with an empty key: err = %v, want ErrNotSignedIn", err)
	}
}

// A failing MCP call names its status and the body's reason, and never the key
// that body can repeat.
func TestZaiReportsAFailureWithoutTheKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":"invalid API key zai-key"}`)
	}))
	defer server.Close()

	zai := Zai{BaseURL: server.URL, Key: func(context.Context) (string, error) { return "zai-key", nil }}
	_, err := zai.Search(t.Context(), Query{Text: "go"})
	if err == nil || !strings.HasPrefix(err.Error(), "zai answered HTTP 503: ") {
		t.Fatalf("err = %v, want the status and the body's reason", err)
	}
	if strings.Contains(err.Error(), "zai-key") {
		t.Fatalf("err = %v, want the key redacted", err)
	}
}

// An MCP error keeps its code, and its message never carries the key.
func TestZaiReportsAnMCPErrorWithoutTheKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.Contains(string(raw), "initialize") {
			w.Header().Set("Mcp-Session-Id", "session-1")
			_, _ = io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n")
			return
		}
		_, _ = io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"id\":2,\"error\":{\"code\":1001,\"message\":\"missing auth for zai-key\"}}\n\n")
	}))
	defer server.Close()

	zai := Zai{BaseURL: server.URL, Key: func(context.Context) (string, error) { return "zai-key", nil }}
	_, err := zai.Search(t.Context(), Query{Text: "go"})
	if err == nil || !strings.Contains(err.Error(), "1001") || !strings.Contains(err.Error(), "missing auth") {
		t.Fatalf("err = %v, want the MCP code and message", err)
	}
	if strings.Contains(err.Error(), "zai-key") {
		t.Fatalf("err = %v, want the key redacted", err)
	}
}
