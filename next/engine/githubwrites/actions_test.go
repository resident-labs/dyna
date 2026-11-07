package githubwrites

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestActionsRoutes(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body, run string
		allowed                       bool
	}{
		{"own dispatch", "POST", "workflows/ci.yml/dispatches", `{"ref":"work","inputs":{"suite":"unit"}}`, "", true},
		{"heads dispatch", "POST", "workflows/12/dispatches", `{"ref":"refs/heads/work"}`, "", true},
		{"main", "POST", "workflows/12/dispatches", `{"ref":"main"}`, "", false},
		{"other session", "POST", "workflows/12/dispatches", `{"ref":"other"}`, "", false},
		{"tag", "POST", "workflows/12/dispatches", `{"ref":"refs/tags/work"}`, "", false},
		{"sha", "POST", "workflows/12/dispatches", `{"ref":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, "", false},
		{"extra field", "POST", "workflows/12/dispatches", `{"ref":"work","other":true}`, "", false},
		{"missing ref", "POST", "workflows/12/dispatches", `{}`, "", false},
		{"rerun", "POST", "runs/7/rerun", `{}`, `{"head_branch":"work","head_repository":{"full_name":"ACME/App"}}`, true},
		{"rerun failed", "POST", "runs/7/rerun-failed-jobs", `{}`, `{"head_branch":"work","head_repository":{"full_name":"acme/app"}}`, true},
		{"cancel", "POST", "runs/7/cancel", `{}`, `{"head_branch":"work","head_repository":{"full_name":"acme/app"}}`, true},
		{"other run", "POST", "runs/7/rerun", `{}`, `{"head_branch":"other","head_repository":{"full_name":"acme/app"}}`, false},
		{"fork", "POST", "runs/7/rerun", `{}`, `{"head_branch":"work","head_repository":{"full_name":"fork/app"}}`, false},
		{"malformed", "POST", "runs/7/cancel", `{}`, `not json`, false},
		{"missing repository", "POST", "runs/7/cancel", `{}`, `{"head_branch":"work"}`, false},
		{"delete logs", "DELETE", "runs/7/logs", `{}`, "", false},
		{"approve", "POST", "runs/7/approve", `{}`, "", false},
		{"deployments", "POST", "runs/7/pending_deployments", `{}`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, up := newGuard()
			up.reads["/repos/acme/app/actions/runs/7"] = tc.run
			configureActionsGuard(g)
			req, _ := http.NewRequestWithContext(WithSession(WithAllowed(context.Background(), grantOf("acme/app", "actions")), "ses_own"), tc.method, "https://api.github.com/repos/acme/app/actions/"+tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := g.RoundTrip(req)
			if resp != nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed = %v, want %v: %v", err == nil, tc.allowed, err)
			}
			for i, sent := range up.sent {
				if !strings.HasPrefix(sent, "GET ") && (!tc.allowed || !up.vouched[i]) {
					t.Fatalf("unauthorized or unvouched forwarding: %s", sent)
				}
			}
		})
	}
}

func configureActionsGuard(g *Guard) {
	g.CanPush = func(ctx context.Context, repo contract.RepoName, branch string) (bool, error) {
		return SessionFrom(ctx) == "ses_own" && repo == "acme/app" && branch == "work", nil
	}
}

func TestActionsAuthorizationFailsClosed(t *testing.T) {
	for _, name := range []string{"missing caller", "nil checker", "store error", "group off", "writes off", "repo off", "lookup status", "lookup transport"} {
		t.Run(name, func(t *testing.T) {
			g, up := newGuard()
			configureActionsGuard(g)
			up.reads["/repos/acme/app/actions/runs/7"] = `{"head_branch":"work","head_repository":{"full_name":"acme/app"}}`
			grant := grantOf("acme/app", contract.GitHubWriteActions)
			caller := contract.SessionID("ses_own")
			lookups := 0
			if name == "missing caller" {
				caller = ""
			}
			if name == "nil checker" {
				g.CanPush = nil
			}
			if name == "store error" {
				g.CanPush = func(context.Context, contract.RepoName, string) (bool, error) {
					return true, fmt.Errorf("store unavailable")
				}
			}
			if name == "group off" {
				grant = grantOf("acme/app", contract.GitHubWriteIssues)
			}
			if name == "writes off" {
				grant = contract.GitHubWrites{}
			}
			if name == "repo off" {
				grant = grantOf("acme/other", contract.GitHubWriteActions)
			}
			base := g.Base
			g.Base = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method == "GET" {
					lookups++
					if name == "lookup transport" {
						return nil, fmt.Errorf("unavailable")
					}
					if name == "lookup status" {
						return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
					}
				}
				return base.RoundTrip(r)
			})
			req, _ := http.NewRequestWithContext(WithSession(WithAllowed(context.Background(), grant), caller), "POST", "https://api.github.com/repos/acme/app/actions/runs/7/rerun", nil)
			response, err := g.RoundTrip(req)
			if response != nil {
				response.Body.Close()
			}
			if err == nil {
				t.Fatal("write passed")
			}
			if name == "missing caller" || name == "nil checker" || name == "group off" || name == "writes off" || name == "repo off" {
				if lookups != 0 {
					t.Fatal("authorization lookup before caller/grant checks")
				}
			}
			for _, sent := range up.sent {
				if !strings.HasPrefix(sent, "GET ") {
					t.Fatalf("write forwarded: %s", sent)
				}
			}
		})
	}
}

func TestActionsGroupOffAllRoutes(t *testing.T) {
	for _, path := range []string{"workflows/ci.yml/dispatches", "runs/7/rerun", "runs/7/rerun-failed-jobs", "runs/7/cancel"} {
		g, up := newGuard()
		g.CanPush = func(context.Context, contract.RepoName, string) (bool, error) {
			t.Fatal("group off reached branch lookup")
			return true, nil
		}
		got := rest(t, g, grantOf("acme/app", contract.GitHubWriteIssues), "POST", "/repos/acme/app/actions/"+path, `{"ref":"work"}`)
		expect(t, path, got, contract.GitHubWriteGroupOff)
		if len(up.sent) != 0 {
			t.Fatal("group off reached upstream")
		}
	}
}

func TestActionsRejectsNonBranchRefsEvenIfRecorded(t *testing.T) {
	for _, ref := range []string{"refs/tags/work", "refs/heads/refs/tags/work", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "refs/heads/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "", "refs/pull/7/head"} {
		g, up := newGuard()
		g.CanPush = func(context.Context, contract.RepoName, string) (bool, error) {
			t.Fatal("non-branch reached push lookup")
			return true, nil
		}
		data, _ := json.Marshal(map[string]string{"ref": ref})
		req, _ := http.NewRequestWithContext(WithSession(WithAllowed(context.Background(), grantOf("acme/app", contract.GitHubWriteActions)), "ses_own"), "POST", "https://api.github.com/repos/acme/app/actions/workflows/ci.yml/dispatches", strings.NewReader(string(data)))
		req.Header.Set("Content-Type", "application/json")
		response, err := g.RoundTrip(req)
		if response != nil {
			response.Body.Close()
		}
		if err == nil || len(up.sent) != 0 {
			t.Fatalf("non-branch %q was forwarded", ref)
		}
	}
}
