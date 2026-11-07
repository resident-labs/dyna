package githubmcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/githubwrites"
)

func TestActionsToolCalls(t *testing.T) {
	for _, method := range []string{"run_workflow", "rerun_workflow_run", "rerun_failed_jobs", "cancel_workflow_run", "delete_workflow_run_logs"} {
		t.Run(method, func(t *testing.T) {
			var writes atomic.Int32
			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == "GET" {
					fmt.Fprint(w, `{"head_branch":"work","head_repository":{"full_name":"o/r"}}`)
					return
				}
				writes.Add(1)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer fake.Close()
			target, _ := url.Parse(fake.URL)
			handler, err := Handler(context.Background(), "fixture-token", &toFake{target: target}, nil, func(ctx context.Context, repo contract.RepoName, branch string) (bool, error) {
				return githubwrites.SessionFrom(ctx) == "own" && repo == "o/r" && branch == "work", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handler.ServeHTTP(w, r.WithContext(githubwrites.WithSession(githubwrites.WithAllowed(r.Context(), contract.GitHubWrites{Repos: []contract.RepoName{"o/r"}, Groups: []contract.GitHubWriteGroup{contract.GitHubWriteActions}}), "own")))
			}))
			defer server.Close()
			ctx := context.Background()
			session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			if _, err := session.ListTools(ctx, nil); err != nil {
				t.Fatal(err)
			}
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "actions_run_trigger", Arguments: map[string]any{"method": method, "owner": "o", "repo": "r", "ref": "work", "workflow_id": "ci.yml", "run_id": 7}})
			want := int32(1)
			if method == "delete_workflow_run_logs" {
				want = 0
			}
			if writes.Load() != want {
				t.Fatalf("writes = %d, want %d; result %+v, err %v", writes.Load(), want, result, err)
			}
			if want == 0 && (err != nil || result == nil || !result.IsError) {
				t.Fatalf("delete logs did not return a tool refusal: %+v, %v", result, err)
			}
			if want == 1 && (err != nil || result == nil || result.IsError) {
				t.Fatalf("call failed: %+v, %v", result, err)
			}
		})
	}
}
