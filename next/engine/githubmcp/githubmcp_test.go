package githubmcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/githubwrites"
	"github.com/resident-labs/dyna/engine/prs"
)

// toFake sends what would go to GitHub's hosts to the fake, and notes who it claimed to be.
type toFake struct {
	target *url.URL
	mu     sync.Mutex
	auth   []string
}

func (f *toFake) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.auth = append(f.auth, r.URL.Host+" "+r.Header.Get("Authorization"))
	f.mu.Unlock()
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host, r.Host = f.target.Scheme, f.target.Host, f.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

// fakeGitHub answers the few REST reads these tests make, each with the core quota it is set to report.
type fakeGitHub struct {
	mu        sync.Mutex
	remaining int
	reset     time.Time
}

func (g *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	remaining, reset := g.remaining, g.reset
	g.mu.Unlock()
	resource := "core"
	if strings.HasPrefix(r.URL.Path, "/search/") {
		resource = "search"
	}
	w.Header().Set("X-RateLimit-Resource", resource)
	w.Header().Set("X-RateLimit-Limit", "5000")
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/search/code":
		fmt.Fprintf(w, `{"total_count":1,"incomplete_results":false,"items":[{"name":"main.rs","path":"src/needle/main.rs","sha":"abc","repository":{"full_name":"o/r"}}]}`)
	case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/branches":
		fmt.Fprintf(w, `[{"name":"main","commit":{"sha":"abc"},"protected":false}]`)
	default:
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"message":"Not Found","documentation_url":"https://docs.github.com/rest","status":"404"}`)
	}
}

func (g *fakeGitHub) set(remaining int, reset time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.remaining, g.reset = remaining, reset
}

func connect(t *testing.T) (*mcp.ClientSession, *fakeGitHub, *toFake, *prs.Client) {
	t.Helper()
	ctx := context.Background()
	fake := &fakeGitHub{remaining: 4999, reset: time.Now().Add(time.Hour)}
	github := httptest.NewServer(fake)
	t.Cleanup(github.Close)
	target, _ := url.Parse(github.URL)
	rewrite := &toFake{target: target}
	pulls := &prs.Client{Token: "engine-token", Endpoint: &url.URL{Scheme: "https", Host: "api.github.com"}, Transport: rewrite, Now: time.Now}
	handler, err := Handler(ctx, "engine-token", pulls.Reads(rewrite))
	if err != nil {
		t.Fatal(err)
	}
	// The box sends no credential: the engine adds its own.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("the box sent a credential")
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "pi", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/github/"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	// A client learns from the list which arguments it repeats as headers, as newer MCP versions ask.
	if _, err := session.ListTools(ctx, nil); err != nil {
		t.Fatal(err)
	}
	return session, fake, rewrite, pulls
}

func text(result *mcp.CallToolResult) string {
	var parts []string
	for _, item := range result.Content {
		if block, ok := item.(*mcp.TextContent); ok {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// Every tool is one of upstream's reads, and every read the owner asked for is there.
func TestToolsAreUpstreamsReads(t *testing.T) {
	session, _, _, _ := connect(t)
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s is not read-only", tool.Name)
		}
	}
	slices.Sort(names)
	t.Logf("%d tools: %s", len(names), strings.Join(names, " "))
	for _, want := range []string{
		"search_code", "search_repositories", "search_issues", "search_pull_requests", "search_commits", "search_users", "search_orgs",
		"get_file_contents", "get_repository_tree", "get_file_blame", "list_branches", "list_tags", "get_tag", "list_commits", "get_commit",
		"list_pull_requests", "pull_request_read", "list_issues", "issue_read", "issue_dependency_read", "find_duplicate",
		"actions_list", "actions_get", "get_job_logs", "list_releases", "get_latest_release", "get_release_by_tag", "get_me",
	} {
		if !slices.Contains(names, want) {
			t.Errorf("%s is missing", want)
		}
	}
	for _, write := range []string{"issue_write", "create_pull_request", "merge_pull_request", "add_issue_comment", "create_or_update_file", "push_files", "delete_file", "create_branch", "actions_run_trigger", "update_pull_request", "ui_get"} {
		if slices.Contains(names, write) {
			t.Errorf("%s is offered", write)
		}
	}
}

// A read goes out with the engine's token, a write tool does not exist, and a failed read says what failed.
func TestReadsUseTheEngineToken(t *testing.T) {
	session, _, rewrite, _ := connect(t)
	ctx := context.Background()
	found, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search_code", Arguments: map[string]any{"query": "needle repo:o/r"}})
	if err != nil || found.IsError || !strings.Contains(text(found), "src/needle/main.rs") {
		t.Fatalf("search_code: %v %+v", err, found)
	}
	rewrite.mu.Lock()
	auth := slices.Clone(rewrite.auth)
	rewrite.mu.Unlock()
	if len(auth) == 0 || !slices.ContainsFunc(auth, func(line string) bool { return line == "api.github.com Bearer engine-token" }) {
		t.Fatalf("GitHub was asked as %q", auth)
	}

	written, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "issue_write", Arguments: map[string]any{"method": "create", "owner": "o", "repo": "r", "title": "x"}})
	if err == nil && (written == nil || !written.IsError) {
		t.Fatalf("a write tool answered: %+v", written)
	}
	t.Logf("issue_write: %v %v", err, written)

	missing, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "pull_request_read", Arguments: map[string]any{"method": "get_files", "owner": "o", "repo": "r", "pullNumber": 99}})
	if err != nil || !missing.IsError || !strings.Contains(text(missing), "404") || strings.Contains(text(missing), "goroutine") || strings.Contains(text(missing), "engine-token") {
		t.Fatalf("a missing PR: %v %+v", err, missing)
	}
	t.Logf("missing PR: %s", text(missing))
}

// Under 40% of the quota, a read says when agents may read again, and asks GitHub nothing.
func TestLowQuotaSaysWhen(t *testing.T) {
	session, fake, rewrite, pulls := connect(t)
	ctx := context.Background()
	reset := time.Now().Add(20 * time.Minute).Truncate(time.Second)
	fake.set(1500, reset)
	first, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_branches", Arguments: map[string]any{"owner": "o", "repo": "r"}})
	if err != nil || first.IsError || !strings.Contains(text(first), "main") {
		t.Fatalf("list_branches: %v %+v", err, first)
	}
	rewrite.mu.Lock()
	before := len(rewrite.auth)
	rewrite.mu.Unlock()
	held, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_branches", Arguments: map[string]any{"owner": "o", "repo": "r", "page": 2}})
	if err != nil || !held.IsError || !strings.Contains(text(held), "wait until "+reset.UTC().Format(time.RFC3339)) {
		t.Fatalf("under 40%%: %v %+v", err, held)
	}
	t.Logf("under 40%%: %s", text(held))
	rewrite.mu.Lock()
	after := len(rewrite.auth)
	rewrite.mu.Unlock()
	if after != before {
		t.Fatalf("a held read reached GitHub")
	}
	if _, low := pulls.Low(); low {
		t.Fatal("the feeds slowed at 30%")
	}
}

// The tools a box is offered are the reads and the write tools of the groups its room has on, and a PR edit's schema has no
// argument the transport refuses.
func TestWriteToolsFollowTheRoomsGroups(t *testing.T) {
	ctx := context.Background()
	handler, err := Handler(ctx, "engine-token", http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	var grant contract.GitHubWrites
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(githubwrites.WithAllowed(r.Context(), grant)))
	}))
	t.Cleanup(server.Close)
	list := func(g contract.GitHubWrites) map[string]*mcp.Tool {
		grant = g
		session, err := mcp.NewClient(&mcp.Implementation{Name: "pi", Version: "test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()
		listed, err := session.ListTools(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		tools := map[string]*mcp.Tool{}
		for _, tool := range listed.Tools {
			tools[tool.Name] = tool
		}
		return tools
	}
	writes := func(tools map[string]*mcp.Tool) []string {
		var names []string
		for name, tool := range tools {
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				names = append(names, name)
			}
		}
		slices.Sort(names)
		return names
	}
	if got := writes(list(contract.GitHubWrites{})); len(got) != 0 {
		t.Fatalf("setting off offers %v", got)
	}
	for _, group := range []contract.GitHubWriteGroup{contract.GitHubWriteIssues, contract.GitHubWritePulls, contract.GitHubWriteProjects} {
		grant := contract.GitHubWrites{Repos: []contract.RepoName{"o/r"}, Groups: []contract.GitHubWriteGroup{group}}
		want := githubwrites.ToolNames(grant)
		slices.Sort(want)
		if got := writes(list(grant)); !slices.Equal(got, want) || len(want) == 0 {
			t.Errorf("%s offers %v, want %v", group, got, want)
		}
	}
	all := contract.GitHubWrites{Repos: []contract.RepoName{"o/r"}, Groups: []contract.GitHubWriteGroup{contract.GitHubWriteIssues, contract.GitHubWritePulls, contract.GitHubWriteProjects}}
	tools := list(all)
	for _, never := range []string{"merge_pull_request", "create_or_update_file", "push_files", "delete_file", "create_branch", "actions_run_trigger", "create_pull_request", "create_repository_ruleset"} {
		if tools[never] != nil {
			t.Errorf("%s is offered with every group on", never)
		}
	}
	// The listed descriptions say that labels are created and edited: issue_write creates a label it does not know by name.
	for name, said := range map[string]string{"issue_write": "created as a new label", "label_write": "creates and edits labels"} {
		if tools[name] == nil || !strings.Contains(tools[name].Description, said) {
			t.Errorf("%s has no %q in its description", name, said)
		}
	}
	if only := list(contract.GitHubWrites{Repos: []contract.RepoName{"o/r"}, Groups: []contract.GitHubWriteGroup{contract.GitHubWriteIssues}})["issue_write"]; only == nil || !strings.Contains(only.Description, "created as a new label") {
		t.Errorf("issue_write under issues alone has no label creation in its description")
	}
	edit := tools["update_pull_request"]
	if edit == nil {
		t.Fatal("pulls offers no update_pull_request")
	}
	properties := edit.InputSchema.(map[string]any)["properties"].(map[string]any)
	for _, param := range githubwrites.StrippedParams["update_pull_request"] {
		if _, ok := properties[param]; ok {
			t.Errorf("update_pull_request offers %s", param)
		}
	}
	for _, param := range []string{"title", "body"} {
		if _, ok := properties[param]; !ok {
			t.Errorf("update_pull_request lacks %s", param)
		}
	}
}
