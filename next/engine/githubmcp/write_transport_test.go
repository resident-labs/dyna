package githubmcp

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/githubwrites"
	"github.com/resident-labs/dyna/engine/prs"
)

// Exercise the real Handler's signed REST/ETag and GraphQL/features clients, write guard and quota/read transport. Most cases
// report the live token's quota (3898/5000); a separate low-quota case checks a local refusal, not a failed GitHub response.
func TestPullDraftStateThroughRealTransport(t *testing.T) {
	for _, tt := range []struct {
		name         string
		draft        bool
		gzip         bool
		repeat       bool
		remaining    int
		node         string
		lookupStatus int
		lookupBody   string
		wantRefusal  contract.GitHubWriteReason
		wantDetail   string
	}{
		{name: "ready"},
		{name: "draft", draft: true},
		{name: "gzip and ETag revalidation", gzip: true, repeat: true},
		{name: "local quota wait", remaining: 1999, wantRefusal: contract.GitHubWriteGraphQLUnresolved, wantDetail: "1999 of 5000 GraphQL requests are left"},
		{name: "another repository", node: `{"__typename":"PullRequest","repository":{"nameWithOwner":"o/other"}}`, wantRefusal: contract.GitHubWriteRepoNotAllowed},
		{name: "wrong node type", node: `{"__typename":"Issue","repository":{"nameWithOwner":"o/r"}}`, wantRefusal: contract.GitHubWriteRepoNotAllowed},
		{name: "missing node", node: `null`, wantRefusal: contract.GitHubWriteGraphQLUnresolved, wantDetail: "GitHub returned no node"},
		{name: "lookup forbidden", lookupStatus: http.StatusForbidden, lookupBody: `{"message":"Forbidden"}`, wantRefusal: contract.GitHubWriteGraphQLUnresolved, wantDetail: "GitHub lookup returned HTTP 403 Forbidden"},
		{name: "lookup invalid JSON", lookupBody: `not json`, wantRefusal: contract.GitHubWriteGraphQLUnresolved, wantDetail: "GitHub lookup returned invalid JSON"},
		{name: "lookup GraphQL error", lookupBody: `{"errors":[{"message":"Could not resolve to a node"}]}`, wantRefusal: contract.GitHubWriteGraphQLUnresolved, wantDetail: "GitHub returned GraphQL error: Could not resolve to a node"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var lookups, mutations, notModified int
			reset := time.Now().Add(time.Hour).Truncate(time.Second)
			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer engine-token" {
					t.Errorf("GitHub request missing engine auth: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/pulls/7" {
					w.Header().Set("X-RateLimit-Resource", "core")
					w.Header().Set("X-RateLimit-Limit", "5000")
					w.Header().Set("X-RateLimit-Remaining", "4805")
					w.Header().Set("X-RateLimit-Reset", fmt.Sprint(reset.Unix()))
					if r.Header.Get("If-None-Match") == `"pr-v1"` {
						mu.Lock()
						notModified++
						mu.Unlock()
						w.WriteHeader(http.StatusNotModified)
						return
					}
					w.Header().Set("ETag", `"pr-v1"`)
					fmt.Fprintf(w, `{"id":7,"node_id":"PR_ok","number":7,"draft":%t}`, tt.draft)
					return
				}
				if r.Method != http.MethodPost || r.URL.Path != "/graphql" {
					t.Errorf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				remaining := tt.remaining
				if remaining == 0 {
					remaining = 3898
				}
				w.Header().Set("X-RateLimit-Resource", "graphql")
				w.Header().Set("X-RateLimit-Limit", "5000")
				w.Header().Set("X-RateLimit-Remaining", fmt.Sprint(remaining))
				w.Header().Set("X-RateLimit-Reset", fmt.Sprint(reset.Unix()))
				var body struct {
					Query     string
					Variables struct {
						ID    string
						Input struct{ PullRequestID string }
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("GraphQL body: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				switch {
				case strings.Contains(body.Query, "node(id:"):
					mu.Lock()
					lookups++
					mu.Unlock()
					if body.Variables.ID != "PR_ok" {
						t.Errorf("looked up node %q, want PR_ok", body.Variables.ID)
					}
					if tt.lookupStatus != 0 {
						w.WriteHeader(tt.lookupStatus)
					}
					if tt.lookupBody != "" {
						fmt.Fprint(w, tt.lookupBody)
						return
					}
					node := tt.node
					if node == "" {
						node = `{"__typename":"PullRequest","repository":{"nameWithOwner":"o/r"}}`
					}
					var out io.Writer = w
					if tt.gzip {
						if r.Header.Get("Accept-Encoding") != "gzip" {
							t.Errorf("expected implicit gzip negotiation, got %q", r.Header.Get("Accept-Encoding"))
						}
						w.Header().Set("Content-Encoding", "gzip")
						compressed := gzip.NewWriter(w)
						defer compressed.Close()
						out = compressed
					}
					fmt.Fprintf(out, `{"data":{"node":%s}}`, node)
				case strings.HasPrefix(body.Query, "mutation"):
					mu.Lock()
					mutations++
					mu.Unlock()
					name := "markPullRequestReadyForReview"
					if tt.draft {
						name = "convertPullRequestToDraft"
					}
					if !strings.Contains(strings.ReplaceAll(body.Query, " ", ""), name+"(input:$input)") || body.Variables.Input.PullRequestID != "PR_ok" {
						t.Errorf("unexpected mutation: %+v", body)
					}
					fmt.Fprintf(w, `{"data":{%q:{"pullRequest":{"id":"PR_ok","isDraft":%t}}}}`, name, tt.draft)
				default:
					fmt.Fprintf(w, `{"data":{"repository":{"pullRequest":{"id":"PR_ok","isDraft":%t}}}}`, !tt.draft)
				}
			}))
			t.Cleanup(fake.Close)
			target, _ := url.Parse(fake.URL)
			rewrite := &toFake{target: target}
			pulls := &prs.Client{Token: "engine-token", Endpoint: &url.URL{Scheme: "https", Host: "api.github.com"}, Transport: rewrite, Now: time.Now}
			handler, err := Handler(context.Background(), "engine-token", pulls.Reads(rewrite), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			grant := contract.GitHubWrites{Repos: []contract.RepoName{"o/r"}, Groups: []contract.GitHubWriteGroup{contract.GitHubWritePulls}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" {
					t.Error("the box sent a credential")
				}
				handler.ServeHTTP(w, r.WithContext(githubwrites.WithAllowed(r.Context(), grant)))
			}))
			t.Cleanup(server.Close)
			ctx := context.Background()
			session, err := mcp.NewClient(&mcp.Implementation{Name: "pi", Version: "test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL}, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = session.Close() })
			if _, err := session.ListTools(ctx, nil); err != nil {
				t.Fatal(err)
			}
			calls := 1
			if tt.repeat {
				calls = 2
			}
			for range calls {
				result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "update_pull_request", Arguments: map[string]any{"owner": "o", "repo": "r", "pullNumber": 7, "draft": tt.draft}})
				if err != nil {
					t.Fatal(err)
				}
				if tt.wantRefusal != "" {
					if !result.IsError || !strings.Contains(text(result), string(tt.wantRefusal)) || !strings.Contains(text(result), tt.wantDetail) {
						t.Errorf("want %s with detail %q, got %s", tt.wantRefusal, tt.wantDetail, text(result))
					}
				} else if result.IsError {
					t.Errorf("draft:%t: %s", tt.draft, text(result))
				}
			}
			mu.Lock()
			defer mu.Unlock()
			wantLookups, wantMutations := calls, calls
			if tt.wantRefusal != "" {
				wantMutations = 0
			}
			if tt.remaining == 1999 {
				wantLookups = 0
			}
			if lookups != wantLookups || mutations != wantMutations {
				t.Errorf("lookups %d, mutations %d; want %d, %d", lookups, mutations, wantLookups, wantMutations)
			}
			if tt.repeat && notModified != 1 {
				t.Errorf("ETag revalidations = %d, want 1", notModified)
			}
		})
	}
}
