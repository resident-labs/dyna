//go:build linux

package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/githubmcp"
	"github.com/resident-labs/dyna/engine/prs"
)

// liveGitHub gives a service the GitHub tools the engine builds, with a real token, against real GitHub.
//
// DYNA_GITHUB_TEST_TOKEN is a GitHub token, as the engine holds one. DYNA_GITHUB_TEST_REPO is a repository it can read,
// owner/name, with closed PRs, issues with comments and finished Actions runs; horizon-rust/horizon when unset.
func liveGitHub(t *testing.T, s *Service) (owner, repo string, pulls *prs.Client) {
	t.Helper()
	token := os.Getenv("DYNA_GITHUB_TEST_TOKEN")
	if token == "" {
		t.Skip("DYNA_GITHUB_TEST_TOKEN is a GitHub token; this test reads real GitHub")
	}
	name := os.Getenv("DYNA_GITHUB_TEST_REPO")
	if name == "" {
		name = "horizon-rust/horizon"
	}
	owner, repo, _ = strings.Cut(name, "/")
	pulls = &prs.Client{Token: token, Endpoint: &url.URL{Scheme: "https", Host: "api.github.com"}, Transport: http.DefaultTransport, Now: time.Now}
	tools, err := githubmcp.Handler(context.Background(), token, pulls.Reads(http.DefaultTransport), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.SetGitHubTools(tools)
	return owner, repo, pulls
}

// githubReads is a codemode script that reads one repository the way an agent would: a file, a closed PR's files, and the log of
// a finished Actions job, each found from the one before. It returns what each read gave, cut short.
func githubReads(owner, repo string) string {
	return `const repo = { owner: "` + owner + `", repo: "` + repo + `" };
const text = (r) => (r.content ?? []).map((c) => c.text ?? c.resource?.text ?? "").join("\n");
const out = {};
const step = async (name, tool, args) => {
	const r = await tools[tool]({ ...repo, ...args });
	out[name] = { error: r.isError === true, size: text(r).length, text: text(r).slice(0, 300) };
	return text(r);
};
await step("file", "mcp__github__get_file_contents", { path: "README.md" });
const pulls = await step("pulls", "mcp__github__list_pull_requests", { state: "closed", perPage: 1 });
const pr = Number(/"number":\s*(\d+)/.exec(pulls)?.[1]);
await step("prFiles", "mcp__github__pull_request_read", { method: "get_files", pullNumber: pr, perPage: 5 });
const runs = await step("runs", "mcp__github__actions_list", { method: "list_workflow_runs", perPage: 10, workflow_runs_filter: { status: "completed", event: "push" } });
let run, job;
for (const [, id] of runs.matchAll(/"id":\s*(\d+),\s*"name"/g)) {
	const jobs = await step("jobs", "mcp__github__actions_list", { method: "list_workflow_jobs", resource_id: id, perPage: 1 });
	job = Number(/"id":\s*(\d+)/.exec(jobs.replace(/"total_count":\s*\d+/, ""))?.[1]);
	if (job) { run = id; break; }
}
await step("log", "mcp__github__get_job_logs", { job_id: job, return_content: true, tail_lines: 15 });
out.ids = { pr, run, job };
return JSON.stringify(out);`
}

// githubIssue is a codemode script that finds an issue with comments by search and reads it and its comments.
func githubIssue(owner, repo string) string {
	return `const repo = { owner: "` + owner + `", repo: "` + repo + `" };
const text = (r) => (r.content ?? []).map((c) => c.text ?? c.resource?.text ?? "").join("\n");
const out = {};
const step = async (name, tool, args) => {
	const r = await tools[tool](args);
	out[name] = { error: r.isError === true, size: text(r).length, text: text(r).slice(0, 300) };
	return text(r);
};
await step("code", "mcp__github__search_code", { query: "fn main repo:` + owner + `/` + repo + `", perPage: 3, fields: ["path"] });
const issues = await step("issues", "mcp__github__list_issues", { ...repo, state: "CLOSED", perPage: 20 });
const numbers = [...issues.matchAll(/"number":\s*(\d+)[^{}]*?"comments":\s*([1-9]\d*)/g)].map((m) => Number(m[1]));
const number = numbers[0] ?? Number(/"number":\s*(\d+)/.exec(issues)?.[1]);
await step("issue", "mcp__github__issue_read", { ...repo, method: "get", issue_number: number });
await step("comments", "mcp__github__issue_read", { ...repo, method: "get_comments", issue_number: number, perPage: 3 });
out.number = number;
return JSON.stringify(out);`
}

type readStep struct {
	Error bool   `json:"error"`
	Size  int    `json:"size"`
	Text  string `json:"text"`
}

// stepsOf decodes what a script returned, from a tool result as the model got it.
func stepsOf(t *testing.T, result string) map[string]json.RawMessage {
	t.Helper()
	start, end := strings.Index(result, "{"), strings.LastIndex(result, "}")
	if start < 0 || end < start {
		t.Fatalf("no script result in %q", result)
	}
	body := result[start : end+1]
	var unquoted string
	if strings.HasPrefix(body, `{\"`) && json.Unmarshal([]byte(`"`+body+`"`), &unquoted) == nil {
		body = unquoted
	}
	var steps map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &steps); err != nil {
		t.Fatalf("script result %q: %v", body, err)
	}
	return steps
}

func checkSteps(t *testing.T, steps map[string]json.RawMessage, names ...string) {
	t.Helper()
	for _, name := range names {
		var step readStep
		if err := json.Unmarshal(steps[name], &step); err != nil || step.Error || step.Size == 0 {
			t.Errorf("%s: %s", name, steps[name])
			continue
		}
		t.Logf("%s: %d characters: %.200q", name, step.Size, step.Text)
	}
}

// A session in a real box reads real GitHub through the engine: tool_search finds the github server's tools, a direct call
// searches code, a script reads a file, a PR's files and an Actions job's log, and a write is refused. The box holds no token.
func TestGitHubReadsInARealBox(t *testing.T) {
	provider := &toolUpstream{tools: map[string][]crewTool{}}
	b := newToolBox(t, boxProfiles{}, provider)
	owner, repo, pulls := liveGitHub(t, b.s)
	provider.tools["task-search"] = []crewTool{{"tool_search", jsonOf(t, map[string]string{"query": "github search code"})}, {"mcp__github__search_code", jsonOf(t, map[string]any{"query": "fn main repo:" + owner + "/" + repo, "perPage": 3, "fields": []string{"path"}})}}
	provider.tools["task-reads"] = []crewTool{{"codemode", jsonOf(t, map[string]string{"code": githubReads(owner, repo)})}}
	write := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"issue_write","arguments":{"method":"create","owner":"` + owner + `","repo":"` + repo + `","title":"dyna read-only check"}}}`
	provider.tools["task-write"] = []crewTool{{"bash", jsonOf(t, map[string]string{"command": "curl -s http://127.0.0.1:7781/github/ -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' -d '" + write + "'; echo; env | grep -ci token; ls ~/.config/gh 2>&1 | head -1"})}}
	b.start(t)

	results := b.task(t, "task-search")
	first := provider.firstBody()
	if !strings.Contains(first, "Read-only GitHub through the engine") {
		t.Fatal("the prompt does not name the github server")
	}
	if strings.Contains(first, `"mcp__github__search_code"`) {
		t.Fatal("github tools are declared before tool_search loads them")
	}
	search, found := results[len(results)-2], results[len(results)-1]
	if !strings.Contains(search.Text, "mcp__github__search_code") {
		t.Fatalf("tool_search: %+v", search)
	}
	if found.IsError || !strings.Contains(found.Text, ".rs") {
		t.Fatalf("search_code: %+v", found)
	}
	t.Logf("tool_search loaded: %.300q", search.Text)
	t.Logf("search_code: %.300q", found.Text)

	read := provider.lastOf(b.task(t, "task-reads"))
	steps := stepsOf(t, read.Text)
	checkSteps(t, steps, "file", "pulls", "prFiles", "runs", "jobs", "log")
	t.Logf("ids: %s", steps["ids"])

	refused := provider.lastOf(b.task(t, "task-write"))
	t.Logf("a write from the box: %.400q", refused.Text)
	if !strings.Contains(refused.Text, "issue_write") || !strings.Contains(strings.ToLower(refused.Text), "unknown tool") {
		t.Fatalf("a write: %+v", refused)
	}
	if until, low := pulls.Low(); low {
		t.Logf("the quota is low until %v", until)
	}
}

// A crew worker gets the same tools through the same path: a one-worker workflow searches code and reads an issue with its
// comments.
func TestGitHubReadsInARealCrew(t *testing.T) {
	c := newRealCrew(t, nil)
	owner, repo, _ := liveGitHub(t, c.s)
	c.upstream.tools["task-gh"] = []crewTool{{"tool_search", jsonOf(t, map[string]string{"query": "github issue comments"})}, {"codemode", jsonOf(t, map[string]string{"code": githubIssue(owner, repo)})}}
	script := `export const meta = {
	name: 'github-reads',
	description: 'One worker reads GitHub',
	profiles: ['fast'],
	size: 'medium',
}
phase('read')
const found = await agent('Work on task-gh', { profile: 'fast', label: 'task-gh' })
return { found }
`
	run := c.start(contract.Principal{Kind: contract.PrincipalSession, Session: c.root}, script, "")
	c.wait("the run's end", func() bool { return c.status(run.Run).Run.State != contract.WorkflowRunning })
	final := c.status(run.Run)
	if final.Run.State != contract.WorkflowDone || final.Run.Failed != 0 {
		t.Fatalf("run %+v", final.Run)
	}
	system, messages := c.upstream.lastBody("task-gh")
	if !strings.Contains(system+messages, "Read-only GitHub through the engine") {
		t.Fatal("the worker's prompt does not name the github server")
	}
	if !strings.Contains(messages, "mcp__github__issue_read") {
		t.Fatalf("tool_search did not load issue_read: %.2000s", messages)
	}
	var history []struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal([]byte(messages), &history); err != nil {
		t.Fatal(err)
	}
	var blocks []struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(history[len(history)-1].Content, &blocks); err != nil {
		t.Fatal(err)
	}
	var last string
	for _, block := range blocks {
		var parts []struct {
			Text string `json:"text"`
		}
		if block.Type != "tool_result" {
			continue
		}
		if json.Unmarshal(block.Content, &parts) == nil {
			for _, part := range parts {
				last += part.Text
			}
		} else {
			_ = json.Unmarshal(block.Content, &last)
		}
	}
	steps := stepsOf(t, last)
	checkSteps(t, steps, "code", "issues", "issue", "comments")
	t.Logf("issue: %s", steps["number"])
}
