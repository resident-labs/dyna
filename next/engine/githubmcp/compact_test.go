package githubmcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/githubwrites"
)

// listings opens the full (/github/) and the agy (/github-agy/) endpoint of one handler, with every write group on or none.
func listings(t *testing.T, writes bool) (full, agy *mcp.ClientSession) {
	t.Helper()
	ctx := context.Background()
	handler, err := Handler(ctx, "engine-token", http.DefaultTransport, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var grant contract.GitHubWrites
	if writes {
		grant = contract.GitHubWrites{Repos: []contract.RepoName{"o/r"}, Groups: contract.AllGitHubWriteGroups}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(githubwrites.WithAllowed(r.Context(), grant)))
	}))
	t.Cleanup(server.Close)
	open := func(path string) *mcp.ClientSession {
		session, err := mcp.NewClient(&mcp.Implementation{Name: "agy", Version: "test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + path}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = session.Close() })
		return session
	}
	return open("/github/"), open(contract.GitHubAgyPath)
}

func listed(t *testing.T, session *mcp.ClientSession) map[string]*mcp.Tool {
	t.Helper()
	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := map[string]*mcp.Tool{}
	for _, tool := range result.Tools {
		tools[tool.Name] = tool
	}
	return tools
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// withoutHeaders drops the x-mcp-header hints upstream puts on owner and repo for header-aware proxies, which the agy listing leaves out.
func withoutHeaders(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, value := range v {
			if key != "x-mcp-header" {
				out[key] = withoutHeaders(value)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, value := range v {
			out[i] = withoutHeaders(value)
		}
		return out
	}
	return v
}

// Every tool a box can see through the agy listing, with the writes setting off and with every group on, declares at most maxTool
// bytes and no property description, and keeps what the model calls it by: names, types, enums, required. The full listing keeps
// its text and has no help tool.
func TestAgyListingIsCompactForEveryTool(t *testing.T) {
	for _, writes := range []bool{false, true} {
		full, agy := listings(t, writes)
		fullTools, agyTools := listed(t, full), listed(t, agy)
		if fullTools[helpName] != nil {
			t.Fatalf("writes %v: %s is in the full listing", writes, helpName)
		}
		if agyTools[helpName] == nil {
			t.Fatalf("writes %v: no %s", writes, helpName)
		}
		largest := 0
		for name, tool := range agyTools {
			data, _ := json.Marshal(map[string]any{"name": tool.Name, "description": tool.Description, "inputSchema": tool.InputSchema})
			if len(data) > maxTool {
				t.Errorf("writes %v: %s declares %d bytes", writes, name, len(data))
			}
			largest = max(largest, len(data))
			schema := asMap(t, tool.InputSchema)
			if !reflect.DeepEqual(bare(schema), schema) {
				t.Errorf("%s keeps a property description: %s", name, data)
			}
			if name == helpName {
				continue
			}
			whole := fullTools[name]
			if whole == nil {
				t.Errorf("%s is only in the agy listing", name)
				continue
			}
			want := withoutHeaders(bare(asMap(t, whole.InputSchema)))
			if terse := (name == "projects_write"); terse && !reflect.DeepEqual(withoutShapes(want), withoutShapes(schema)) || !terse && !reflect.DeepEqual(want, schema) {
				t.Errorf("%s: compact schema differs from the full one beyond descriptions", name)
			}
			if first, _, _ := strings.Cut(whole.Description, "\n"); !strings.HasPrefix(tool.Description, strings.TrimSuffix(first, "…")[:min(len(first), firstLine-1)]) {
				t.Errorf("%s: description %q does not start with the full one's first line", name, tool.Description)
			}
			if !reflect.DeepEqual(whole.Annotations, tool.Annotations) {
				t.Errorf("%s: annotations differ", name)
			}
		}
		if len(agyTools) != len(fullTools)+1 {
			t.Errorf("writes %v: %d tools, full listing has %d", writes, len(agyTools), len(fullTools))
		}
		t.Logf("writes %v: %d tools, largest %d bytes", writes, len(agyTools), largest)
	}
}

// github_help answers each tool's whole description and every field's description, including what the first line leaves out.
func TestHelpAnswersTheFullText(t *testing.T) {
	full, agy := listings(t, true)
	ctx := context.Background()
	for name, tool := range listed(t, full) {
		got, err := agy.CallTool(ctx, &mcp.CallToolParams{Name: helpName, Arguments: map[string]any{"tool": name}})
		if err != nil || got.IsError {
			t.Fatalf("%s: %v %+v", name, err, got)
		}
		help := text(got)
		if !strings.Contains(help, tool.Description) {
			t.Errorf("%s: help lacks the full description", name)
		}
		properties, _ := asMap(t, tool.InputSchema)["properties"].(map[string]any)
		for field, value := range properties {
			if description, _ := value.(map[string]any)["description"].(string); description != "" && !strings.Contains(help, field+" (") && !strings.Contains(help, field+": ") || description != "" && !strings.Contains(help, description) {
				t.Errorf("%s: help lacks field %s: %q", name, field, description)
			}
		}
	}
	for tool, want := range map[string]string{"search_issues": "query", "pull_request_read": "get_files", "actions_list": "jobs is the list itself", "projects_write": "pass item_id or node_id", "projects_write ": "update_project_items", " projects_write": "updated_field.value", "projects_write  ": "items[] each is: "} {
		got, _ := agy.CallTool(ctx, &mcp.CallToolParams{Name: helpName, Arguments: map[string]any{"tool": strings.TrimSpace(tool)}})
		if got == nil || !strings.Contains(text(got), want) {
			t.Errorf("%s help lacks %q: %v", tool, want, got)
		}
	}
	unknown, err := agy.CallTool(ctx, &mcp.CallToolParams{Name: helpName, Arguments: map[string]any{"tool": "nope"}})
	if err != nil || !unknown.IsError || !strings.Contains(text(unknown), "search_code") || !strings.Contains(text(unknown), "projects_write") {
		t.Fatalf("unknown tool: %v %+v", err, unknown)
	}
	all, err := agy.CallTool(ctx, &mcp.CallToolParams{Name: helpName, Arguments: map[string]any{}})
	if err != nil || all.IsError || !strings.Contains(text(all), "search_code: ") || strings.Contains(text(all), "ui_get") {
		t.Fatalf("all tools: %v %+v", err, all)
	}
}

// A call is the same handler on both paths, and a refused call on the agy path carries the tool's help.
func TestAgyCallsAreTheFullOnesAndRefusalsCarryHelp(t *testing.T) {
	full, agy := listings(t, false)
	listed(t, full) // a client learns the headers it repeats from the list
	listed(t, agy)
	ctx := context.Background()
	args := map[string]any{"owner": "o", "repo": "r", "pullNumber": 99, "method": "get_files"}
	a, errA := full.CallTool(ctx, &mcp.CallToolParams{Name: "pull_request_read", Arguments: args})
	b, errB := agy.CallTool(ctx, &mcp.CallToolParams{Name: "pull_request_read", Arguments: args})
	if (errA == nil) != (errB == nil) || a == nil || b == nil || a.IsError != b.IsError {
		t.Fatalf("full %v %+v, agy %v %+v", errA, a, errB, b)
	}
	bad := map[string]any{"owner": "o", "repo": "r", "pullNumber": 99}
	_, plain := full.CallTool(ctx, &mcp.CallToolParams{Name: "pull_request_read", Arguments: bad})
	refused, helped := agy.CallTool(ctx, &mcp.CallToolParams{Name: "pull_request_read", Arguments: bad})
	t.Logf("full: %v\nagy: %v", plain, helped)
	if refused != nil {
		t.Logf("agy text: %s", text(refused))
	}
	var message string
	if helped != nil {
		message = helped.Error()
	} else if refused != nil {
		message = text(refused)
	}
	if !strings.Contains(message, "get_files") {
		t.Fatalf("the refusal carries no help: %q", message)
	}
	wrong := map[string]any{"owner": "o", "repo": "r", "pullNumber": "ninety-nine", "method": "get"}
	mistyped, err := agy.CallTool(ctx, &mcp.CallToolParams{Name: "pull_request_read", Arguments: wrong})
	message = ""
	if err != nil {
		message = err.Error()
	} else {
		message = text(mistyped)
	}
	t.Logf("mistyped: %.200s", message)
	if !strings.Contains(message, "get_files") {
		t.Fatalf("a mistyped argument carries no help: %q", message)
	}
}

// withoutShapes keeps each property's name and type and nothing under it, which is all a terse listing promises.
func withoutShapes(schema any) any {
	object := schema.(map[string]any)
	properties, _ := object["properties"].(map[string]any)
	names := map[string]any{}
	for name, value := range properties {
		names[name], _ = value.(map[string]any)["type"]
	}
	return map[string]any{"properties": names, "required": object["required"]}
}
