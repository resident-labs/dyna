package githubmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/githubwrites"
)

// upstream stands for GitHub: it answers the few reads these tests make and counts what is not a read. Nothing here reaches api.github.com.
type upstream struct {
	mu     sync.Mutex
	writes []string
}

func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method != http.MethodGet:
		u.mu.Lock()
		u.writes = append(u.writes, r.Method+" "+r.URL.Path)
		u.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/repos/o/r/pulls/99/files":
		fmt.Fprint(w, `[{"filename":"a.go","status":"modified","additions":3,"deletions":1,"changes":4,"sha":"abc"}]`)
	case r.URL.Path == "/repos/o/r/actions/runs/7/jobs":
		fmt.Fprint(w, `{"total_count":1,"jobs":[{"id":5,"name":"next","status":"completed","conclusion":"failure"}]}`)
	default:
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found","status":"404"}`)
	}
}

// listings opens the full (/github/) and the agy (/github-agy/) endpoint of one handler over the fake upstream, with every write group on or none.
func listings(t *testing.T, writes bool) (full, agy *mcp.ClientSession, fake *upstream) {
	t.Helper()
	ctx := context.Background()
	fake = &upstream{}
	github := httptest.NewServer(fake)
	t.Cleanup(github.Close)
	target, _ := url.Parse(github.URL)
	handler, err := Handler(ctx, "engine-token", &toFake{target: target}, nil, nil)
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
	return open("/github/"), open(contract.GitHubAgyPath), fake
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
		full, agy, _ := listings(t, writes)
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

// github_help answers each tool's whole description and the schema upstream wrote, parsed back from the help: equal to the full
// listing's, so every description, format, default, required list, enum and nested field is there.
func TestHelpAnswersTheFullText(t *testing.T) {
	full, agy, _ := listings(t, true)
	ctx := context.Background()
	seen := map[string]bool{}
	for name, tool := range listed(t, full) {
		got, err := agy.CallTool(ctx, &mcp.CallToolParams{Name: helpName, Arguments: map[string]any{"tool": name}})
		if err != nil || got.IsError {
			t.Fatalf("%s: %v %+v", name, err, got)
		}
		help := text(got)
		description, schema, found := strings.Cut(help, helpSchema)
		if !found || !strings.Contains(description, tool.Description) {
			t.Errorf("%s: help lacks the full description or its schema", name)
			continue
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(schema), &parsed); err != nil {
			t.Fatalf("%s: help schema: %v", name, err)
		}
		if !reflect.DeepEqual(withoutHeaders(parsed), withoutHeaders(asMap(t, tool.InputSchema))) {
			a, _ := json.Marshal(withoutHeaders(parsed))
			b, _ := json.Marshal(withoutHeaders(asMap(t, tool.InputSchema)))
			t.Errorf("%s: help schema differs:\n%s\n%s", name, a, b)
		}
		properties, _ := parsed["properties"].(map[string]any)
		for _, value := range properties {
			for _, key := range []string{"format", "default"} {
				if _, ok := value.(map[string]any)[key]; ok {
					seen[key] = true
				}
			}
		}
		if _, ok := parsed["required"]; ok {
			seen["required"] = true
		}
	}
	// No GitHub tool declares a format today; the equality above carries one if it ever does.
	for _, key := range []string{"default", "required"} {
		if !seen[key] {
			t.Errorf("no tool has a %s to check in help", key)
		}
	}
	for tool, want := range map[string]string{"search_issues": "query", "pull_request_read": "get_files", "actions_list": "jobs is the list itself", "projects_write": "pass item_id or node_id", "projects_write ": "update_project_items", " projects_write": "\"updated_field\":", "projects_write  ": "\"oneOf\""} {
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

// A call is the same handler on both paths: the same successful payloads (a PR's files, workflow jobs), the same denied write that
// never reaches upstream, and a refused call on the agy path carries the tool's help. TestOwnedPullLifecycleThroughMCP and
// TestWorkflowJobsAreFlatThroughTheServer run on both paths too.
func TestAgyCallsAreTheFullOnes(t *testing.T) {
	full, agy, fake := listings(t, true)
	listed(t, full) // a client learns the headers it repeats from the list
	listed(t, agy)
	ctx := context.Background()
	call := func(session *mcp.ClientSession, name string, args map[string]any) (string, bool) {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			return err.Error(), true
		}
		return text(result), result.IsError
	}
	for name, c := range map[string]struct {
		args map[string]any
		want string
	}{
		"pull_request_read": {map[string]any{"owner": "o", "repo": "r", "pullNumber": 99, "method": "get_files"}, "a.go"},
		"actions_list":      {map[string]any{"owner": "o", "repo": "r", "method": "list_workflow_jobs", "resource_id": "7"}, `"jobs":[{`},
	} {
		want, wantErr := call(full, name, c.args)
		got, gotErr := call(agy, name, c.args)
		if wantErr || gotErr || got != want || !strings.Contains(got, c.want) {
			t.Errorf("%s: full %q (error %v), agy %q (error %v)", name, want, wantErr, got, gotErr)
		}
	}
	// A write to a repository outside the room's grant is refused the same way on both paths, and nothing reaches upstream.
	denied := map[string]any{"method": "create", "owner": "o", "repo": "other", "title": "x"}
	want, wantErr := call(full, "issue_write", denied)
	got, gotErr := call(agy, "issue_write", denied)
	if !wantErr || !gotErr || want != got || len(fake.writes) != 0 {
		t.Errorf("denied write: full %q (%v), agy %q (%v), upstream saw %v", want, wantErr, got, gotErr, fake.writes)
	}
	// A refused call on the agy path carries the tool's help; a mistyped argument too.
	for _, bad := range []map[string]any{
		{"owner": "o", "repo": "r", "pullNumber": 99},
		{"owner": "o", "repo": "r", "pullNumber": "ninety-nine", "method": "get"},
	} {
		message, _ := call(agy, "pull_request_read", bad)
		if !strings.Contains(message, "get_files") {
			t.Errorf("%v: the refusal carries no help: %.300s", bad, message)
		}
	}
}

// withoutShapes keeps each property's name and type and nothing under it, which is all a terse listing promises.
func withoutShapes(schema any) any {
	object := schema.(map[string]any)
	properties, _ := object["properties"].(map[string]any)
	names := map[string]any{}
	for name, value := range properties {
		names[name] = value.(map[string]any)["type"]
	}
	return map[string]any{"properties": names, "required": object["required"]}
}
