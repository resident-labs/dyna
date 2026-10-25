//go:build linux

package shellmcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/websearch"
)

// The shell tools keep their positions: harness clients call them by index.
func TestToolListKeepsTheShellToolsFirst(t *testing.T) {
	list := newTools(t, 0).tools()
	if len(list) != 5 || list[0]["name"] != "shell" || list[1]["name"] != "shell_output" {
		t.Fatalf("tools = %v, want shell and shell_output first", names(list))
	}
	search := list[4]
	schema, ok := search["inputSchema"].(map[string]any)
	if !ok || search["name"] != "web_search" {
		t.Fatalf("last tool = %v, want web_search", search)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || properties["query"] == nil || properties["limit"] == nil {
		t.Fatalf("web_search schema = %v, want a query and a limit", schema)
	}
	if required, _ := schema["required"].([]string); len(required) != 1 || required[0] != "query" {
		t.Fatalf("web_search requires %v, want only the query", schema["required"])
	}
}

func TestWebSearchToolAnswersWithCompactJSON(t *testing.T) {
	tools := newTools(t, 0)
	var asked callspec.WebSearchRequest
	tools.Search = func(_ context.Context, in callspec.WebSearchRequest) (callspec.WebSearchResponse, error) {
		asked = in
		return callspec.WebSearchResponse{Results: []websearch.Result{
			{Title: "Go", URL: "https://go.dev", Snippet: "The Go language.", Provider: "brave"},
		}}, nil
	}
	answer := call(t, tools, "web_search", map[string]any{"query": "  go language  ", "limit": 3})
	if asked.Query != "go language" || asked.Limit != 3 {
		t.Fatalf("engine asked for %+v, want the trimmed query and the limit", asked)
	}
	want := `[{"title":"Go","url":"https://go.dev","snippet":"The Go language.","provider":"brave"}]`
	if answer != want {
		t.Fatalf("answer = %s, want %s", answer, want)
	}
}

// A refusal from the engine reaches the session as the tool's own failure.
func TestWebSearchToolReportsAOneLineFailure(t *testing.T) {
	tools := newTools(t, 0)
	tools.Search = func(context.Context, callspec.WebSearchRequest) (callspec.WebSearchResponse, error) {
		return callspec.WebSearchResponse{}, errors.New("web search found no provider: chatgpt is not signed in; brave is not signed in")
	}
	raw, _ := json.Marshal(map[string]any{"query": "go"})
	sent := tools.Call(t.Context(), "web_search", raw, "")
	if !sent.Failed || sent.Text != "web search found no provider: chatgpt is not signed in; brave is not signed in" {
		t.Fatalf("result = %+v, want the engine's one-line reason", sent)
	}
}

func TestWebSearchToolNeedsAQuery(t *testing.T) {
	tools := newTools(t, 0)
	asked := false
	tools.Search = func(context.Context, callspec.WebSearchRequest) (callspec.WebSearchResponse, error) {
		asked = true
		return callspec.WebSearchResponse{}, nil
	}
	raw, _ := json.Marshal(map[string]any{"query": "   "})
	if sent := tools.Call(t.Context(), "web_search", raw, ""); !sent.Failed || asked {
		t.Fatalf("result = %+v, asked = %v; want a refusal without a search", sent, asked)
	}
}

// A shared OpenCode server serves each session from its own tools. The search
// must reach the engine for that call too.
func TestWebSearchSurvivesThePerSessionTools(t *testing.T) {
	tools := newTools(t, 0)
	asked := ""
	tools.Search = func(_ context.Context, in callspec.WebSearchRequest) (callspec.WebSearchResponse, error) {
		asked = in.Query
		return callspec.WebSearchResponse{}, nil
	}
	tools.Resolve = func(string) (Caller, bool) { return Caller{Owner: "other-session"}, true }
	raw, _ := json.Marshal(map[string]any{"query": "go"})
	if sent := tools.Call(t.Context(), "web_search", raw, "harness-session"); sent.Failed || asked != "go" {
		t.Fatalf("result = %+v, asked = %q; want the shared server's search", sent, asked)
	}
}

func names(list []map[string]any) []string {
	out := make([]string, len(list))
	for i, tool := range list {
		out[i], _ = tool["name"].(string)
	}
	return out
}
