package githubwrites

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/prs"
)

// upstream stands in for GitHub behind the guard: it keeps what reached it and answers a node lookup from its table.
type upstream struct {
	mu      sync.Mutex
	sent    []string
	vouched []bool            // for each write that reached GitHub, whether the guard vouched for it
	nodes   map[string]string // node ID to the JSON of its node object
	prs     map[string]bool   // "owner/repo#n" that are PRs
	reads   map[string]string // path of a GET to the JSON it answers, ahead of every other answer
}

func (u *upstream) RoundTrip(r *http.Request) (*http.Response, error) {
	var data []byte
	if r.Body != nil {
		data, _ = io.ReadAll(r.Body)
	}
	reply := func(body string) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	}
	if r.Method == http.MethodPost && strings.Contains(string(data), "node(id:$id)") {
		var asked struct {
			Query     string
			Variables struct{ ID string }
		}
		_ = json.Unmarshal(data, &asked)
		node, ok := u.nodes[asked.Variables.ID]
		if !ok {
			return reply(`{"data":{"node":null}}`)
		}
		// GitHub answers a union member's fields only when the query has a fragment on that member, so an item's content keeps
		// its repository only when the query asks for it on the content's own type.
		var item struct {
			Content map[string]any `json:"content"`
		}
		_ = json.Unmarshal([]byte(node), &item)
		_, askedContent, _ := strings.Cut(asked.Query, "content{")
		if item.Content != nil && !strings.Contains(askedContent, "... on "+item.Content["__typename"].(string)+"{repository{nameWithOwner}}") {
			var whole map[string]any
			_ = json.Unmarshal([]byte(node), &whole)
			delete(whole["content"].(map[string]any), "repository")
			stripped, _ := json.Marshal(whole)
			node = string(stripped)
		}
		return reply(`{"data":{"node":` + node + `}}`)
	}
	if body, ok := u.reads[r.URL.Path]; ok && r.Method == http.MethodGet {
		return reply(body)
	}
	if r.Method == http.MethodGet && (strings.Contains(r.URL.Path, "/comments/") || strings.HasPrefix(r.URL.Path, "/orgs/") || strings.HasPrefix(r.URL.Path, "/users/")) {
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{}}, nil
	}
	if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues/") && u.prs != nil {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/repos/"), "/")
		if u.prs[parts[0]+"/"+parts[1]+"#"+parts[3]] {
			return reply(`{"pull_request":{}}`)
		}
		return reply(`{}`)
	}
	u.mu.Lock()
	u.sent = append(u.sent, r.Method+" "+r.URL.Path)
	u.vouched = append(u.vouched, prs.IsVouched(r.Context()))
	u.mu.Unlock()
	return reply(`{}`)
}

func newGuard() (*Guard, *upstream) {
	up := &upstream{nodes: map[string]string{
		"R_ok":        `{"__typename":"Repository","nameWithOwner":"acme/app"}`,
		"R_other":     `{"__typename":"Repository","nameWithOwner":"acme/secret"}`,
		"I_ok":        `{"__typename":"Issue","repository":{"nameWithOwner":"acme/app"}}`,
		"I_other":     `{"__typename":"Issue","repository":{"nameWithOwner":"acme/secret"}}`,
		"PR_ok":       `{"number":7,"__typename":"PullRequest","repository":{"nameWithOwner":"acme/app"}}`,
		"PR_other":    `{"__typename":"PullRequest","repository":{"nameWithOwner":"acme/secret"}}`,
		"P_ok":        `{"__typename":"ProjectV2","owner":{"login":"acme"}}`,
		"P_other":     `{"__typename":"ProjectV2","owner":{"login":"stranger"}}`,
		"IT_ok":       `{"__typename":"ProjectV2Item","project":{"owner":{"login":"acme"}},"content":{"__typename":"Issue","repository":{"nameWithOwner":"acme/app"}}}`,
		"IT_other":    `{"__typename":"ProjectV2Item","project":{"owner":{"login":"acme"}},"content":{"__typename":"Issue","repository":{"nameWithOwner":"acme/secret"}}}`,
		"PRR_ok":      `{"__typename":"PullRequestReview","repository":{"nameWithOwner":"acme/app"}}`,
		"IT_draft":    `{"__typename":"ProjectV2Item","project":{"owner":{"login":"acme"}},"content":{"__typename":"DraftIssue"}}`,
		"IT_pr":       `{"__typename":"ProjectV2Item","project":{"owner":{"login":"acme"}},"content":{"__typename":"PullRequest","repository":{"nameWithOwner":"ACME/App"}}}`,
		"IT_pr_other": `{"__typename":"ProjectV2Item","project":{"owner":{"login":"acme"}},"content":{"__typename":"PullRequest","repository":{"nameWithOwner":"acme/secret"}}}`,
		"IT_unknown":  `{"__typename":"ProjectV2Item","project":{"owner":{"login":"acme"}},"content":{"__typename":"Discussion","repository":{"nameWithOwner":"acme/app"}}}`,

		"M_ok":    `{"__typename":"Milestone","repository":{"nameWithOwner":"acme/app"}}`,
		"M_other": `{"__typename":"Milestone","repository":{"nameWithOwner":"acme/secret"}}`,
		"L_ok":    `{"__typename":"Label","repository":{"nameWithOwner":"acme/app"}}`,
		"L_other": `{"__typename":"Label","repository":{"nameWithOwner":"acme/secret"}}`,
	}, prs: map[string]bool{"acme/app#7": true}, reads: map[string]string{
		"/repos/acme/app/issues/comments/3": `{"issue_url":"https://api.github.com/repos/acme/app/issues/3"}`,
		"/repos/acme/app/issues/comments/4": `{"issue_url":"https://api.github.com/repos/acme/secret/issues/3"}`,
		"/repos/acme/app/issues/comments/5": `{"issue_url":"https://evil.example/repos/acme/app/issues/3"}`,
		"/repos/acme/app/issues/comments/6": `{}`,
		"/repos/acme/app/issues/comments/7": `{"issue_url":"https://api.github.com/repos/ACME/App/issues/3"}`,
		"/orgs/acme/projectsV2/4/items/9":   `{"content_type":"Issue","content":{"repository_url":"https://api.github.com/repos/acme/app"}}`,
		"/orgs/acme/projectsV2/4/items/10":  `{"content_type":"Issue","content":{"repository_url":"https://api.github.com/repos/acme/secret"}}`,
		"/orgs/acme/projectsV2/4/items/11":  `{"content_type":"DraftIssue","content":null}`,
		"/orgs/acme/projectsV2/4/items/13":  `{"content_type":"PullRequest","content":{"url":"https://api.github.com/repos/ACME/App/pulls/5","issue_url":"https://api.github.com/repos/acme/app/issues/5"}}`,
		"/orgs/acme/projectsV2/4/items/14":  `{"content_type":"PullRequest","content":{"url":"https://api.github.com/repos/acme/secret/pulls/5"}}`,
		"/orgs/acme/projectsV2/4/items/15":  `{"content_type":"Discussion","content":{"repository_url":"https://api.github.com/repos/acme/app","url":"https://api.github.com/repos/acme/app/pulls/5"}}`,
		"/orgs/acme/projectsV2/4/items/16":  `{"content_type":"PullRequest","content":{}}`,
		"/orgs/acme/projectsV2/4/items/17":  `{"content_type":"Issue","content":{"url":"https://api.github.com/repos/acme/app/pulls/5"}}`,
		"/users/acme/projectsV2/4/items/9":  `{"content_type":"Issue","content":{"repository_url":"https://api.github.com/repos/acme/app"}}`,
	}}
	return &Guard{Base: up, API: &url.URL{Scheme: "https", Host: "api.github.com"}}, up
}

var everyGroup = []contract.GitHubWriteGroup{contract.GitHubWriteIssues, contract.GitHubWritePulls, contract.GitHubWriteProjects}

func grantOf(repos string, groups ...contract.GitHubWriteGroup) contract.GitHubWrites {
	var grant contract.GitHubWrites
	for _, repo := range strings.Split(repos, ",") {
		grant.Repos = append(grant.Repos, contract.RepoName(repo))
	}
	grant.Groups = groups
	return grant
}

// do sends a request through the guard with a room's grant on its context, and returns the typed reason of a refusal, or "" when
// the write reached GitHub.
func do(t *testing.T, g *Guard, grant contract.GitHubWrites, method, target, contentType, body string) contract.GitHubWriteReason {
	t.Helper()
	request, err := http.NewRequestWithContext(WithAllowed(context.Background(), grant), method, target, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := g.RoundTrip(request)
	var refusal *Refusal
	if errors.As(err, &refusal) {
		return refusal.Reason
	}
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	response.Body.Close()
	return ""
}

func rest(t *testing.T, g *Guard, grant contract.GitHubWrites, method, path, body string) contract.GitHubWriteReason {
	t.Helper()
	return do(t, g, grant, method, "https://api.github.com"+path, "application/json", body)
}

func graphql(t *testing.T, g *Guard, grant contract.GitHubWrites, query, variables string) contract.GitHubWriteReason {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"query": query, "variables": json.RawMessage(variables)})
	return do(t, g, grant, http.MethodPost, "https://api.github.com/graphql", "application/json", string(body))
}

// expect fails the test when a write's outcome is not the reason wanted; "" means it reached GitHub.
func expect(t *testing.T, name string, got, want contract.GitHubWriteReason) {
	t.Helper()
	if got != want {
		t.Errorf("%s: refused as %q, want %q", name, got, want)
	}
}

// With the setting off, every write is refused and every read passes.
func TestSettingOffRefusesEveryWrite(t *testing.T) {
	g, up := newGuard()
	off := contract.GitHubWrites{}
	expect(t, "issue create", rest(t, g, off, "POST", "/repos/acme/app/issues", `{"title":"x"}`), contract.GitHubWriteOff)
	expect(t, "mutation", graphql(t, g, off, `mutation($i:CloseIssueInput!){closeIssue(input:$i){clientMutationId}}`, `{"i":{"issueId":"I_ok"}}`), contract.GitHubWriteOff)
	groupsNoRepos := contract.GitHubWrites{Groups: everyGroup}
	expect(t, "groups with no repos", rest(t, g, groupsNoRepos, "POST", "/repos/acme/app/issues", `{"title":"x"}`), contract.GitHubWriteOff)
	read, _ := http.NewRequest("GET", "https://api.github.com/repos/acme/app/issues", nil)
	if _, err := g.RoundTrip(read); err != nil {
		t.Fatalf("a read: %v", err)
	}
	if len(up.sent) != 1 {
		t.Fatalf("reached GitHub: %v", up.sent)
	}
}

// Each group allows its own writes and nothing more.
func TestEachGroupAllowsItsOwnWritesOnly(t *testing.T) {
	type write struct {
		name, method, path, body string
		query, variables         string
		group                    contract.GitHubWriteGroup
	}
	writes := []write{
		{name: "issue create", method: "POST", path: "/repos/acme/app/issues", body: `{"title":"x","labels":["a"]}`, group: contract.GitHubWriteIssues},
		{name: "issue edit", method: "PATCH", path: "/repos/acme/app/issues/3", body: `{"title":"y","state":"closed"}`, group: contract.GitHubWriteIssues},
		{name: "issue comment", method: "POST", path: "/repos/acme/app/issues/3/comments", body: `{"body":"x"}`, group: contract.GitHubWriteIssues},
		{name: "create issue graphql", query: `mutation($i:CreateIssueInput!){createIssue(input:$i){issue{id}}}`, variables: `{"i":{"repositoryId":"R_ok","title":"x"}}`, group: contract.GitHubWriteIssues},
		{name: "update pr", method: "PATCH", path: "/repos/acme/app/pulls/7", body: `{"title":"y","body":"z"}`, group: contract.GitHubWritePulls},
		{name: "pr review comment", method: "POST", path: "/repos/acme/app/pulls/7/comments", body: `{"body":"x","in_reply_to":4}`, group: contract.GitHubWritePulls},
		{name: "review", query: `mutation($i:AddPullRequestReviewInput!){addPullRequestReview(input:$i){pullRequestReview{id}}}`, variables: `{"i":{"pullRequestId":"PR_ok","event":"COMMENT","body":"x"}}`, group: contract.GitHubWritePulls},
		{name: "project item add", query: `mutation($i:AddProjectV2ItemByIdInput!){addProjectV2ItemById(input:$i){item{id}}}`, variables: `{"i":{"projectId":"P_ok","contentId":"I_ok"}}`, group: contract.GitHubWriteProjects},
		{name: "project field set", query: `mutation($i:UpdateProjectV2ItemFieldValueInput!){updateProjectV2ItemFieldValue(input:$i){projectV2Item{id}}}`, variables: `{"i":{"projectId":"P_ok","itemId":"IT_ok","fieldId":"F","value":{"text":"x"}}}`, group: contract.GitHubWriteProjects},
		{name: "project field set rest", method: "PATCH", path: "/orgs/acme/projectsV2/4/items/9", body: `{"fields":[{"id":1,"value":"x"}]}`, group: contract.GitHubWriteProjects},
	}
	for _, only := range everyGroup {
		g, _ := newGuard()
		grant := grantOf("acme/app", only)
		for _, w := range writes {
			var got contract.GitHubWriteReason
			if w.query != "" {
				got = graphql(t, g, grant, w.query, w.variables)
			} else {
				got = rest(t, g, grant, w.method, w.path, w.body)
			}
			want := contract.GitHubWriteGroupOff
			if w.name == "issue edit" && only == contract.GitHubWritePulls {
				want = contract.GitHubWriteFieldNotAllowed // pulls reaches PATCH issues only for labels
			} else if w.name == "issue comment" && only == contract.GitHubWritePulls {
				want = "" // pulls comments through the same route
			} else if w.group == only {
				want = ""
			}
			expect(t, string(only)+" only: "+w.name, got, want)
		}
	}
}

// Merging in every form, and deletes, are refused with every group on.
func TestMergesAndDeletesAreNeverAllowed(t *testing.T) {
	g, up := newGuard()
	g.Owns = func(context.Context, contract.RepoName, int) (bool, error) { return true, nil }
	grant := grantOf("acme/app", everyGroup...)
	for name, got := range map[string]contract.GitHubWriteReason{
		"merge":                rest(t, g, grant, "PUT", "/repos/acme/app/pulls/7/merge", `{}`),
		"rebase":               rest(t, g, grant, "POST", "/repos/acme/app/merges", `{"base":"main","head":"x"}`),
		"update-branch":        rest(t, g, grant, "PUT", "/repos/acme/app/pulls/7/update-branch", `{}`),
		"delete ref":           rest(t, g, grant, "DELETE", "/repos/acme/app/git/refs/heads/x", `{}`),
		"delete issue comment": rest(t, g, grant, "DELETE", "/repos/acme/app/issues/comments/3", `{}`),
		"delete label":         rest(t, g, grant, "DELETE", "/repos/acme/app/issues/3/labels/bug", `{}`),
		"delete repo":          rest(t, g, grant, "DELETE", "/repos/acme/app", `{}`),
		"contents write":       rest(t, g, grant, "PUT", "/repos/acme/app/contents/a.txt", `{"message":"m","content":"eA=="}`),
		"release":              rest(t, g, grant, "POST", "/repos/acme/app/releases", `{"tag_name":"v1"}`),
		"workflow dispatch":    rest(t, g, grant, "POST", "/repos/acme/app/actions/workflows/1/dispatches", `{"ref":"main"}`),
		"secret":               rest(t, g, grant, "PUT", "/repos/acme/app/actions/secrets/X", `{}`),
		"protection":           rest(t, g, grant, "PUT", "/repos/acme/app/branches/main/protection", `{}`),
		"repo settings":        rest(t, g, grant, "PATCH", "/repos/acme/app", `{"private":false}`),
	} {
		expect(t, name, got, contract.GitHubWriteRouteNotAllowed)
	}
	for _, mutation := range []string{"mergePullRequest", "enablePullRequestAutoMerge", "enqueuePullRequest", "disablePullRequestAutoMerge", "deleteIssue", "deleteLabel", "deletePullRequestReview", "deleteProjectV2Item", "deleteProjectV2", "deleteRef", "createRef", "updateRef", "createCommitOnBranch", "addStar", "deleteIssueComment", "requestReviews", "updateRepository", "createRelease", "mergeBranch", "updateBranchProtectionRule"} {
		got := graphql(t, g, grant, `mutation($i:X!){`+mutation+`(input:$i){clientMutationId}}`, `{"i":{"pullRequestId":"PR_ok"}}`)
		expect(t, mutation, got, contract.GitHubWriteGraphQLMutation)
	}
	if len(up.sent) != 0 {
		t.Fatalf("reached GitHub: %v", up.sent)
	}
}

// A repository outside the allowed set is refused: by path, by node ID, by owner, and for a project item by its content.
func TestOtherRepositoriesAreRefused(t *testing.T) {
	g, up := newGuard()
	grant := grantOf("acme/app", everyGroup...)
	expect(t, "rest other repo", rest(t, g, grant, "POST", "/repos/acme/secret/issues", `{"title":"x"}`), contract.GitHubWriteRepoNotAllowed)
	expect(t, "rest other owner", rest(t, g, grant, "POST", "/repos/stranger/app/issues", `{"title":"x"}`), contract.GitHubWriteRepoNotAllowed)
	expect(t, "rest project of another owner", rest(t, g, grant, "PATCH", "/orgs/stranger/projectsV2/4/items/9", `{"fields":[]}`), contract.GitHubWriteRepoNotAllowed)
	expect(t, "createIssue by repository id", graphql(t, g, grant, `mutation($i:CreateIssueInput!){createIssue(input:$i){issue{id}}}`, `{"i":{"repositoryId":"R_other","title":"x"}}`), contract.GitHubWriteRepoNotAllowed)
	expect(t, "unknown id", graphql(t, g, grant, `mutation($i:CreateIssueInput!){createIssue(input:$i){issue{id}}}`, `{"i":{"repositoryId":"R_nope","title":"x"}}`), contract.GitHubWriteGraphQLUnresolved)
	expect(t, "review of another repo's PR", graphql(t, g, grant, `mutation($i:AddPullRequestReviewInput!){addPullRequestReview(input:$i){clientMutationId}}`, `{"i":{"pullRequestId":"PR_other","event":"COMMENT"}}`), contract.GitHubWriteRepoNotAllowed)
	expect(t, "an issue id where a PR is meant", graphql(t, g, grant, `mutation($i:AddPullRequestReviewInput!){addPullRequestReview(input:$i){clientMutationId}}`, `{"i":{"pullRequestId":"I_ok"}}`), contract.GitHubWriteRepoNotAllowed)
	expect(t, "project of another owner", graphql(t, g, grant, `mutation($i:AddProjectV2ItemByIdInput!){addProjectV2ItemById(input:$i){clientMutationId}}`, `{"i":{"projectId":"P_other","contentId":"I_ok"}}`), contract.GitHubWriteRepoNotAllowed)
	expect(t, "adding another repo's issue to a project", graphql(t, g, grant, `mutation($i:AddProjectV2ItemByIdInput!){addProjectV2ItemById(input:$i){clientMutationId}}`, `{"i":{"projectId":"P_ok","contentId":"I_other"}}`), contract.GitHubWriteRepoNotAllowed)
	expect(t, "field set on another repo's item", graphql(t, g, grant, `mutation($i:UpdateProjectV2ItemFieldValueInput!){updateProjectV2ItemFieldValue(input:$i){clientMutationId}}`, `{"i":{"projectId":"P_ok","itemId":"IT_other","fieldId":"F","value":{}}}`), contract.GitHubWriteRepoNotAllowed)
	expect(t, "field set on a draft item", graphql(t, g, grant, `mutation($i:UpdateProjectV2ItemFieldValueInput!){updateProjectV2ItemFieldValue(input:$i){clientMutationId}}`, `{"i":{"projectId":"P_ok","itemId":"IT_draft","fieldId":"F","value":{}}}`), contract.GitHubWriteRepoNotAllowed)
	if len(up.sent) != 0 {
		t.Fatalf("reached GitHub: %v", up.sent)
	}
}

// Even the PR owner cannot edit sensitive fields or bypass the PR guard through the issues route.
func TestPullEditSensitiveFieldsRemainRefused(t *testing.T) {
	g, up := newGuard()
	g.Owns = func(context.Context, contract.RepoName, int) (bool, error) { return true, nil }
	grant := grantOf("acme/app", contract.GitHubWritePulls)
	const pr = "/repos/acme/app/pulls/7"
	for name, body := range map[string]string{
		"draft":                 `{"draft":true}`,
		"invalid state":         `{"state":"merged"}`,
		"null state":            `{"state":null}`,
		"reviewers":             `{"reviewers":["a"]}`,
		"maintainer_can_modify": `{"maintainer_can_modify":false}`,
		"case variant":          `{"Title":"x"}`,
		"an unknown field":      `{"title":"x","assignees":["a"]}`,
		"not an object":         `["title"]`,
	} {
		expect(t, "PATCH pulls "+name, rest(t, g, grant, "PATCH", pr, body), contract.GitHubWriteFieldNotAllowed)
	}
	expect(t, "PATCH the PR's issue with state", rest(t, g, grant, "PATCH", "/repos/acme/app/issues/7", `{"state":"closed"}`), contract.GitHubWriteFieldNotAllowed)
	expect(t, "PATCH the PR's issue with assignees", rest(t, g, grant, "PATCH", "/repos/acme/app/issues/7", `{"labels":["a"],"assignees":["x"]}`), contract.GitHubWriteFieldNotAllowed)
	for name, vars := range map[string]string{
		"state":               `{"i":{"pullRequestId":"PR_ok","state":"CLOSED"}}`,
		"title and state":     `{"i":{"pullRequestId":"PR_ok","title":"x","state":"CLOSED"}}`,
		"maintainerCanModify": `{"i":{"pullRequestId":"PR_ok","maintainerCanModify":false}}`,
	} {
		expect(t, "updatePullRequest "+name, graphql(t, g, grant, `mutation($i:UpdatePullRequestInput!){updatePullRequest(input:$i){clientMutationId}}`, vars), contract.GitHubWriteFieldNotAllowed)
	}
	if len(up.sent) != 0 {
		t.Fatalf("a PR was changed beyond its title, body and labels: %v", up.sent)
	}
	expect(t, "title and body", rest(t, g, grant, "PATCH", pr, `{"title":"x","body":"y"}`), "")
	expect(t, "title only", rest(t, g, grant, "PATCH", pr, `{"title":"x"}`), "")
	expect(t, "labels route", rest(t, g, grant, "POST", "/repos/acme/app/issues/7/labels", `{"labels":["a"]}`), "")
	expect(t, "labels by PATCH on the issue", rest(t, g, grant, "PATCH", "/repos/acme/app/issues/7", `{"labels":["a"]}`), "")
	expect(t, "updatePullRequest title and body", graphql(t, g, grant, `mutation($i:UpdatePullRequestInput!){updatePullRequest(input:$i){clientMutationId}}`, `{"i":{"pullRequestId":"PR_ok","title":"x","body":"y"}}`), "")
	expect(t, "ready for review", graphql(t, g, grant, `mutation($i:MarkPullRequestReadyForReviewInput!){markPullRequestReadyForReview(input:$i){clientMutationId}}`, `{"i":{"pullRequestId":"PR_ok"}}`), "")
	expect(t, "convert to draft", graphql(t, g, grant, `mutation($i:ConvertPullRequestToDraftInput!){convertPullRequestToDraft(input:$i){clientMutationId}}`, `{"i":{"pullRequestId":"PR_ok"}}`), "")
	expect(t, "ready for review with an extra field", graphql(t, g, grant, `mutation($i:MarkPullRequestReadyForReviewInput!){markPullRequestReadyForReview(input:$i){clientMutationId}}`, `{"i":{"pullRequestId":"PR_ok","title":"x"}}`), contract.GitHubWriteFieldNotAllowed)
	if len(up.sent) != 7 {
		t.Fatalf("allowed edits that reached GitHub: %v", up.sent)
	}
}

// With issues on, an issue edit works, and the same call on a PR's number is refused beyond labels, since the issues API reaches a PR.
func TestIssuesGroupDoesNotEditAPullRequest(t *testing.T) {
	g, _ := newGuard()
	grant := grantOf("acme/app", contract.GitHubWriteIssues)
	expect(t, "edit an issue", rest(t, g, grant, "PATCH", "/repos/acme/app/issues/3", `{"state":"closed"}`), "")
	expect(t, "close a PR as an issue", rest(t, g, grant, "PATCH", "/repos/acme/app/issues/7", `{"state":"closed"}`), contract.GitHubWriteFieldNotAllowed)
	expect(t, "closeIssue with a PR's id", graphql(t, g, grant, `mutation($i:CloseIssueInput!){closeIssue(input:$i){clientMutationId}}`, `{"i":{"issueId":"PR_ok"}}`), contract.GitHubWriteRepoNotAllowed)
}

// `*` is the room's repositories, as the grant's Expanded says, and nothing wider.
func TestAllReposMeansTheRoomsRepos(t *testing.T) {
	grant := contract.GitHubWrites{Repos: []contract.RepoName{contract.GitHubWriteAllRepos}, Groups: everyGroup}.Expanded([]contract.RoomRepo{{Repo: "acme/app"}, {Repo: "acme/lib"}})
	g, _ := newGuard()
	expect(t, "room repo", rest(t, g, grant, "POST", "/repos/acme/lib/issues", `{"title":"x"}`), "")
	expect(t, "not a room repo", rest(t, g, grant, "POST", "/repos/acme/secret/issues", `{"title":"x"}`), contract.GitHubWriteRepoNotAllowed)
	if len(grant.Repos) != 2 || grant.Allows("*") {
		t.Fatalf("expanded: %v", grant.Repos)
	}
}

// Paths that look like an allowed route but are not canonical are refused.
func TestPathConfusion(t *testing.T) {
	g, up := newGuard()
	grant := grantOf("acme/app", everyGroup...)
	body := `{"title":"x"}`
	for name, path := range map[string]string{
		"dot-dot":           "/repos/acme/app/../secret/issues",
		"dot-dot encoded":   "/repos/acme/app/%2e%2e/secret/issues",
		"encoded repo":      "/repos/acme/%61pp/issues",
		"encoded slash":     "/repos/acme%2Fsecret/issues",
		"trailing slash":    "/repos/acme/app/issues/",
		"double slash":      "/repos/acme//app/issues",
		"upper literal":     "/repos/acme/app/Issues",
		"upper repos":       "/Repos/acme/app/issues",
		"query":             "/repos/acme/app/issues?repo=secret",
		"fragment":          "/repos/acme/app/issues#x",
		"extra segment":     "/repos/acme/app/issues/extra",
		"issue number text": "/repos/acme/app/issues/abc/comments",
		"semicolon":         "/repos/acme/app/issues;x",
		"space":             "/repos/acme/app/is%20sues",
		"api prefix":        "/api/v3/repos/acme/app/issues",
	} {
		expect(t, name, rest(t, g, grant, "POST", path, body), contract.GitHubWriteRouteNotAllowed)
	}
	// Case in the owner and repository is GitHub's to ignore, so it is compared without case against the allowed set.
	expect(t, "owner and repo case", rest(t, g, grant, "POST", "/repos/ACME/App/issues", body), "")
	other, _ := url.Parse("https://api.github.com.evil.example/repos/acme/app/issues")
	expect(t, "another host", do(t, g, grant, "POST", other.String(), "application/json", body), contract.GitHubWriteRouteNotAllowed)
	expect(t, "upload host", do(t, g, grant, "POST", "https://uploads.github.com/repos/acme/app/issues", "application/json", body), contract.GitHubWriteRouteNotAllowed)
	expect(t, "plain http", do(t, g, grant, "POST", "http://api.github.com/repos/acme/app/issues", "application/json", body), contract.GitHubWriteRouteNotAllowed)
	expect(t, "user info", do(t, g, grant, "POST", "https://acme:x@api.github.com/repos/acme/app/issues", "application/json", body), contract.GitHubWriteRouteNotAllowed)
	if len(up.sent) != 1 {
		t.Fatalf("reached GitHub: %v", up.sent)
	}
}

// A redirect between repositories is a new request, which is checked like the first.
func TestRedirectIsCheckedAgain(t *testing.T) {
	g, up := newGuard()
	grant := grantOf("acme/app", everyGroup...)
	first, _ := http.NewRequest("POST", "https://api.github.com/repos/acme/app/issues", strings.NewReader(`{"title":"x"}`))
	first = first.WithContext(WithAllowed(context.Background(), grant))
	first.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/repos/acme/app/issues" {
			return &http.Response{StatusCode: 307, Header: http.Header{"Location": {"https://api.github.com/repositories/9/issues"}}, Body: http.NoBody, Request: r}, nil
		}
		return g.RoundTrip(r)
	})}
	if _, err := client.Do(first); err == nil {
		t.Fatal("a redirect to a path outside the table was followed")
	}
	if len(up.sent) != 0 {
		t.Fatalf("reached GitHub: %v", up.sent)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Bodies the guard cannot read as the one thing a route takes are refused.
func TestContentTypesAndBodies(t *testing.T) {
	g, _ := newGuard()
	grant := grantOf("acme/app", everyGroup...)
	api := "https://api.github.com"
	expect(t, "form body", do(t, g, grant, "POST", api+"/repos/acme/app/issues", "application/x-www-form-urlencoded", `title=x`), contract.GitHubWriteContentType)
	expect(t, "no content type", do(t, g, grant, "POST", api+"/repos/acme/app/issues", "", `{"title":"x"}`), contract.GitHubWriteContentType)
	expect(t, "text", do(t, g, grant, "POST", api+"/graphql", "text/plain", `{"query":"mutation{x}"}`), contract.GitHubWriteContentType)
	expect(t, "json with charset", do(t, g, grant, "POST", api+"/repos/acme/app/issues", "application/json; charset=utf-8", `{"title":"x"}`), "")
	expect(t, "not json", rest(t, g, grant, "POST", "/repos/acme/app/issues", `title=x`), contract.GitHubWriteBodyUnreadable)
	expect(t, "empty body", rest(t, g, grant, "POST", "/repos/acme/app/issues", ``), contract.GitHubWriteBodyUnreadable)
	expect(t, "too big", rest(t, g, grant, "POST", "/repos/acme/app/issues", `{"title":"`+strings.Repeat("x", 1<<20)+`"}`), contract.GitHubWriteBodyUnreadable)
}

// GraphQL is parsed, not matched: aliases, several operations and batches cannot carry a forbidden mutation past the check.
func TestGraphQLShapes(t *testing.T) {
	g, up := newGuard()
	grant := grantOf("acme/app", everyGroup...)
	closeIssue := `closeIssue(input:$a){clientMutationId}`
	merge := `mergePullRequest(input:$b){clientMutationId}`
	vars := `{"a":{"issueId":"I_ok"},"b":{"pullRequestId":"PR_ok"}}`
	opener := `mutation($a:CloseIssueInput!,$b:MergePullRequestInput!)`
	expect(t, "alias of an allowed mutation", graphql(t, g, grant, opener+`{x:`+closeIssue+`}`, vars), "")
	expect(t, "alias hiding a merge", graphql(t, g, grant, opener+`{closeIssue:`+merge+`}`, vars), contract.GitHubWriteGraphQLMutation)
	expect(t, "allowed and forbidden in one mutation", graphql(t, g, grant, opener+`{a:`+closeIssue+` b:`+merge+`}`, vars), contract.GitHubWriteGraphQLMutation)
	expect(t, "two operations, the second forbidden", graphql(t, g, grant, `mutation One($a:CloseIssueInput!){`+closeIssue+`} mutation Two($b:MergePullRequestInput!){`+merge+`}`, vars), contract.GitHubWriteGraphQLMutation)
	expect(t, "two operations, operationName picks the allowed", func() contract.GitHubWriteReason {
		body, _ := json.Marshal(map[string]any{"query": `mutation One($a:CloseIssueInput!){` + closeIssue + `} mutation Two($b:MergePullRequestInput!){` + merge + `}`, "operationName": "One", "variables": json.RawMessage(vars)})
		return do(t, g, grant, "POST", "https://api.github.com/graphql", "application/json", string(body))
	}(), contract.GitHubWriteGraphQLMutation)
	expect(t, "query beside a mutation", graphql(t, g, grant, `query{viewer{login}} `+opener+`{`+closeIssue+`}`, vars), "")
	expect(t, "fragment at the root", graphql(t, g, grant, opener+`{...F} fragment F on Mutation{`+closeIssue+`}`, vars), contract.GitHubWriteGraphQLShape)
	expect(t, "inline fragment at the root", graphql(t, g, grant, opener+`{... on Mutation{`+closeIssue+`}}`, vars), contract.GitHubWriteGraphQLShape)
	expect(t, "directive", graphql(t, g, grant, opener+`{closeIssue(input:$a)@include(if:true){clientMutationId}}`, vars), contract.GitHubWriteGraphQLShape)
	expect(t, "inline input object", graphql(t, g, grant, `mutation{closeIssue(input:{issueId:"I_other"}){clientMutationId}}`, `{}`), contract.GitHubWriteGraphQLShape)
	expect(t, "extra argument", graphql(t, g, grant, `mutation($a:CloseIssueInput!){closeIssue(input:$a,other:1){clientMutationId}}`, vars), contract.GitHubWriteGraphQLShape)
	expect(t, "variable not given", graphql(t, g, grant, `mutation($a:CloseIssueInput!){closeIssue(input:$a){clientMutationId}}`, `{}`), contract.GitHubWriteGraphQLShape)
	expect(t, "input with an unlisted field", graphql(t, g, grant, `mutation($a:CloseIssueInput!){closeIssue(input:$a){clientMutationId}}`, `{"a":{"issueId":"I_ok","extra":1}}`), contract.GitHubWriteFieldNotAllowed)
	expect(t, "node id missing", graphql(t, g, grant, `mutation($a:CloseIssueInput!){closeIssue(input:$a){clientMutationId}}`, `{"a":{"stateReason":"COMPLETED"}}`), contract.GitHubWriteGraphQLShape)
	expect(t, "subscription", graphql(t, g, grant, `subscription{x}`, `{}`), contract.GitHubWriteGraphQLSubscription)
	expect(t, "subscription with an allowed mutation", graphql(t, g, grant, opener+`{`+closeIssue+`} subscription{x}`, vars), contract.GitHubWriteGraphQLSubscription)
	expect(t, "unparsable", graphql(t, g, grant, `mutation{{{`, `{}`), contract.GitHubWriteGraphQLUnparsable)
	expect(t, "introspection as a mutation root", graphql(t, g, grant, `mutation{__typename}`, `{}`), contract.GitHubWriteGraphQLMutation)
	expect(t, "no query", do(t, g, grant, "POST", "https://api.github.com/graphql", "application/json", `{"extensions":{"persistedQuery":{"sha256Hash":"x","version":1}}}`), contract.GitHubWriteGraphQLPersisted)
	expect(t, "persisted beside a query", do(t, g, grant, "POST", "https://api.github.com/graphql", "application/json", `{"query":"mutation{x}","extensions":{"persistedQuery":{}}}`), contract.GitHubWriteGraphQLPersisted)
	expect(t, "batch", do(t, g, grant, "POST", "https://api.github.com/graphql", "application/json", `[{"query":"mutation{x}"}]`), contract.GitHubWriteGraphQLBatch)
	expect(t, "unknown key", do(t, g, grant, "POST", "https://api.github.com/graphql", "application/json", `{"query":"{viewer{login}}","other":1}`), contract.GitHubWriteFieldNotAllowed)
	expect(t, "not json", do(t, g, grant, "POST", "https://api.github.com/graphql", "application/json", `query{}`), contract.GitHubWriteBodyUnreadable)
	expect(t, "query string on graphql", do(t, g, grant, "POST", "https://api.github.com/graphql?x=1", "application/json", `{"query":"{viewer{login}}"}`), contract.GitHubWriteRouteNotAllowed)
	expect(t, "an unknown mutation", graphql(t, g, grant, `mutation($a:X!){transferRepository(input:$a){clientMutationId}}`, vars), contract.GitHubWriteGraphQLMutation)
	if len(up.sent) != 2 {
		t.Fatalf("what reached GitHub: %v", up.sent)
	}
}

// A GraphQL query is a read: it passes with the setting off, and without the guard's vouch, so the read transport still checks it.
// A mutation is refused with the setting off, and when it is allowed it is vouched for.
func TestGraphQLQueriesPassWithWritesOff(t *testing.T) {
	g, up := newGuard()
	off := contract.GitHubWrites{}
	query := `query($o:String!){repository(owner:$o,name:"r"){discussions(first:1){nodes{id}}}}`
	expect(t, "query, setting off", graphql(t, g, off, query, `{"o":"acme"}`), "")
	expect(t, "query, setting on", graphql(t, g, grantOf("acme/app", everyGroup...), query, `{"o":"acme"}`), "")
	expect(t, "mutation, setting off", graphql(t, g, off, `mutation($i:CloseIssueInput!){closeIssue(input:$i){clientMutationId}}`, `{"i":{"issueId":"I_ok"}}`), contract.GitHubWriteOff)
	expect(t, "query beside a mutation, setting off", graphql(t, g, off, `query{viewer{login}} mutation($i:CloseIssueInput!){closeIssue(input:$i){clientMutationId}}`, `{"i":{"issueId":"I_ok"}}`), contract.GitHubWriteOff)
	expect(t, "subscription, setting off", graphql(t, g, off, `subscription{x}`, `{}`), contract.GitHubWriteGraphQLSubscription)
	expect(t, "unparsable, setting off", graphql(t, g, off, `query{{{`, `{}`), contract.GitHubWriteGraphQLUnparsable)
	expect(t, "allowed mutation", graphql(t, g, grantOf("acme/app", everyGroup...), `mutation($i:CloseIssueInput!){closeIssue(input:$i){clientMutationId}}`, `{"i":{"issueId":"I_ok"}}`), "")
	if !slices.Equal(up.sent, []string{"POST /graphql", "POST /graphql", "POST /graphql"}) || !slices.Equal(up.vouched, []bool{false, false, true}) {
		t.Fatalf("what reached GitHub: %v, vouched %v (queries unvouched, the mutation vouched)", up.sent, up.vouched)
	}
}

// The sub-issue routes take the global ID of the issue to add, which names no repository, so they are not allowed at all: an
// issues grant on one repository cannot re-parent an issue of another.
func TestSubIssueRoutesAreRefused(t *testing.T) {
	g, up := newGuard()
	grant := grantOf("acme/app", everyGroup...)
	expect(t, "add", rest(t, g, grant, "POST", "/repos/acme/app/issues/3/sub_issues", `{"sub_issue_id":99,"replace_parent":true}`), contract.GitHubWriteRouteNotAllowed)
	expect(t, "reprioritize", rest(t, g, grant, "PATCH", "/repos/acme/app/issues/3/sub_issues/priority", `{"sub_issue_id":99,"after_id":1}`), contract.GitHubWriteRouteNotAllowed)
	expect(t, "createIssue with a parent of another repo", graphql(t, g, grant, `mutation($i:CreateIssueInput!){createIssue(input:$i){issue{id}}}`, `{"i":{"repositoryId":"R_ok","title":"x","parentIssueId":"I_other"}}`), contract.GitHubWriteRepoNotAllowed)
	if len(up.sent) != 0 {
		t.Fatalf("reached GitHub: %v", up.sent)
	}
}

// A review under pulls is a comment or a change request. APPROVE can satisfy a required-review protection under the engine's
// token, which is a way to merge, so it is refused on every path that creates or submits a review.
func TestReviewsCannotApprove(t *testing.T) {
	g, up := newGuard()
	grant := grantOf("acme/app", contract.GitHubWritePulls)
	add := func(input string) contract.GitHubWriteReason {
		return graphql(t, g, grant, `mutation($i:AddPullRequestReviewInput!){addPullRequestReview(input:$i){pullRequestReview{id}}}`, `{"i":`+input+`}`)
	}
	submit := func(input string) contract.GitHubWriteReason {
		return graphql(t, g, grant, `mutation($i:SubmitPullRequestReviewInput!){submitPullRequestReview(input:$i){pullRequestReview{id}}}`, `{"i":`+input+`}`)
	}
	const refused = contract.GitHubWriteReviewEvent
	// GraphQL, create: the event is in the input variable.
	expect(t, "add APPROVE", add(`{"pullRequestId":"PR_ok","event":"APPROVE"}`), refused)
	expect(t, "add lowercase approve", add(`{"pullRequestId":"PR_ok","event":"approve"}`), refused)
	expect(t, "add lowercase comment", add(`{"pullRequestId":"PR_ok","event":"comment"}`), refused)
	expect(t, "add DISMISS", add(`{"pullRequestId":"PR_ok","event":"DISMISS"}`), refused)
	expect(t, "add null event", add(`{"pullRequestId":"PR_ok","event":null}`), refused)
	expect(t, "add event that is not a string", add(`{"pullRequestId":"PR_ok","event":["APPROVE"]}`), refused)
	expect(t, "add APPROVE with a body", add(`{"pullRequestId":"PR_ok","event":"APPROVE","body":"lgtm"}`), refused)
	// GraphQL, submit: a pending review needs an event, and it is not APPROVE.
	expect(t, "submit APPROVE", submit(`{"pullRequestId":"PR_ok","pullRequestReviewId":"PRR_ok","event":"APPROVE"}`), refused)
	expect(t, "submit with no event", submit(`{"pullRequestId":"PR_ok","pullRequestReviewId":"PRR_ok"}`), refused)
	expect(t, "submit null event", submit(`{"pullRequestReviewId":"PRR_ok","event":null}`), refused)
	// The event on the document itself, in place of the variable, is not a shape the guard reads.
	expect(t, "inline input", graphql(t, g, grant, `mutation{addPullRequestReview(input:{pullRequestId:"PR_ok",event:APPROVE}){clientMutationId}}`, `{}`), contract.GitHubWriteGraphQLShape)
	// REST: no review route is allowed, so no event reaches GitHub on one, whatever it is.
	for _, event := range []string{"APPROVE", "COMMENT", "REQUEST_CHANGES", ""} {
		body := `{"event":"` + event + `"}`
		if event == "" {
			body = `{}`
		}
		expect(t, "REST create "+event, rest(t, g, grant, "POST", "/repos/acme/app/pulls/7/reviews", body), contract.GitHubWriteRouteNotAllowed)
		expect(t, "REST submit "+event, rest(t, g, grant, "POST", "/repos/acme/app/pulls/7/reviews/12/events", body), contract.GitHubWriteRouteNotAllowed)
	}
	expect(t, "REST dismiss", rest(t, g, grant, "PUT", "/repos/acme/app/pulls/7/reviews/12/dismissals", `{"message":"x"}`), contract.GitHubWriteRouteNotAllowed)
	expect(t, "REST delete pending", rest(t, g, grant, "DELETE", "/repos/acme/app/pulls/7/reviews/12", ``), contract.GitHubWriteRouteNotAllowed)
	expect(t, "GraphQL dismiss", graphql(t, g, grant, `mutation($i:X!){dismissPullRequestReview(input:$i){clientMutationId}}`, `{"i":{"pullRequestReviewId":"PRR_ok","message":"x"}}`), contract.GitHubWriteGraphQLMutation)
	expect(t, "GraphQL delete pending", graphql(t, g, grant, `mutation($i:X!){deletePullRequestReview(input:$i){clientMutationId}}`, `{"i":{"pullRequestReviewId":"PRR_ok"}}`), contract.GitHubWriteGraphQLMutation)
	if len(up.sent) != 0 {
		t.Fatalf("a review that approves or deletes reached GitHub: %v", up.sent)
	}
	// What is allowed still passes: a comment, a change request, a pending review (no event) and its submission.
	expect(t, "add COMMENT", add(`{"pullRequestId":"PR_ok","event":"COMMENT","body":"x"}`), "")
	expect(t, "add REQUEST_CHANGES", add(`{"pullRequestId":"PR_ok","event":"REQUEST_CHANGES","body":"x"}`), "")
	expect(t, "add pending", add(`{"pullRequestId":"PR_ok"}`), "")
	expect(t, "submit COMMENT", submit(`{"pullRequestId":"PR_ok","pullRequestReviewId":"PRR_ok","event":"COMMENT"}`), "")
	expect(t, "submit REQUEST_CHANGES", submit(`{"pullRequestId":"PR_ok","pullRequestReviewId":"PRR_ok","event":"REQUEST_CHANGES","body":"x"}`), "")
	if len(up.sent) != 5 {
		t.Fatalf("allowed reviews that reached GitHub: %v", up.sent)
	}
}

// Repositories match without case, as GitHub names them: whichever spelling the grant, the path or GitHub's node answers has.
func TestRepositoryMatchIgnoresCase(t *testing.T) {
	g, up := newGuard()
	up.nodes["R_cased"] = `{"__typename":"Repository","nameWithOwner":"ACME/APP"}`
	up.nodes["I_cased"] = `{"__typename":"Issue","repository":{"nameWithOwner":"Acme/App"}}`
	for _, spelling := range []string{"acme/app", "ACME/APP", "Acme/App"} {
		grant := grantOf(spelling, everyGroup...)
		for _, path := range []string{"/repos/acme/app/issues", "/repos/ACME/APP/issues", "/repos/Acme/aPP/issues"} {
			expect(t, spelling+" grant, "+path, rest(t, g, grant, "POST", path, `{"title":"x"}`), "")
		}
		expect(t, spelling+" grant, node answer", graphql(t, g, grant, `mutation($i:CreateIssueInput!){createIssue(input:$i){issue{id}}}`, `{"i":{"repositoryId":"R_cased","title":"x"}}`), "")
		expect(t, spelling+" grant, issue node", graphql(t, g, grant, `mutation($i:CloseIssueInput!){closeIssue(input:$i){clientMutationId}}`, `{"i":{"issueId":"I_cased"}}`), "")
		expect(t, spelling+" grant, other repo", rest(t, g, grant, "POST", "/repos/acme/apple/issues", `{"title":"x"}`), contract.GitHubWriteRepoNotAllowed)
	}
}

// A comment is named by a number that says no repository, so the guard reads the comment and requires its issue to be in the
// repository of the path: a comment of another repository is refused whichever group edits it, and so is an answer that names no
// repository, another host or no answer.
func TestCommentEditNeedsTheCommentInTheAllowedRepository(t *testing.T) {
	for _, only := range []contract.GitHubWriteGroup{contract.GitHubWriteIssues, contract.GitHubWritePulls} {
		g, up := newGuard()
		grant := grantOf("acme/app", only)
		const body = `{"body":"x"}`
		expect(t, string(only)+": a comment of the repository", rest(t, g, grant, "PATCH", "/repos/acme/app/issues/comments/3", body), "")
		expect(t, string(only)+": the repository spelled otherwise by GitHub", rest(t, g, grant, "PATCH", "/repos/acme/app/issues/comments/7", body), "")
		expect(t, string(only)+": a comment of another repository", rest(t, g, grant, "PATCH", "/repos/acme/app/issues/comments/4", body), contract.GitHubWriteRepoNotAllowed)
		expect(t, string(only)+": an issue url on another host", rest(t, g, grant, "PATCH", "/repos/acme/app/issues/comments/5", body), contract.GitHubWriteIDUnresolved)
		expect(t, string(only)+": an answer with no issue url", rest(t, g, grant, "PATCH", "/repos/acme/app/issues/comments/6", body), contract.GitHubWriteIDUnresolved)
		expect(t, string(only)+": a comment GitHub does not answer for", rest(t, g, grant, "PATCH", "/repos/acme/app/issues/comments/8", body), contract.GitHubWriteIDUnresolved)
		if len(up.sent) != 2 {
			t.Fatalf("%s: reached GitHub: %v", only, up.sent)
		}
	}
}

// A REST project item update names its item by a number, so the guard reads the item and requires its content to be an issue or
// PR of an allowed repository, the rule the GraphQL field update has; a draft item has none.
func TestProjectItemUpdateNeedsItsContentInTheAllowedRepository(t *testing.T) {
	g, up := newGuard()
	grant := grantOf("acme/app", contract.GitHubWriteProjects)
	const body = `{"fields":[{"id":1,"value":"x"}]}`
	expect(t, "org item of the repository", rest(t, g, grant, "PATCH", "/orgs/acme/projectsV2/4/items/9", body), "")
	expect(t, "user item of the repository", rest(t, g, grant, "PATCH", "/users/acme/projectsV2/4/items/9", body), "")
	expect(t, "item of another repository", rest(t, g, grant, "PATCH", "/orgs/acme/projectsV2/4/items/10", body), contract.GitHubWriteRepoNotAllowed)
	expect(t, "draft item", rest(t, g, grant, "PATCH", "/orgs/acme/projectsV2/4/items/11", body), contract.GitHubWriteRepoNotAllowed)
	expect(t, "item GitHub does not answer for", rest(t, g, grant, "PATCH", "/orgs/acme/projectsV2/4/items/12", body), contract.GitHubWriteIDUnresolved)
	expect(t, "PR item of the repository, named by another case", rest(t, g, grant, "PATCH", "/orgs/acme/projectsV2/4/items/13", body), "")
	expect(t, "PR item of another repository", rest(t, g, grant, "PATCH", "/orgs/acme/projectsV2/4/items/14", body), contract.GitHubWriteRepoNotAllowed)
	expect(t, "content type that is none of the three", rest(t, g, grant, "PATCH", "/orgs/acme/projectsV2/4/items/15", body), contract.GitHubWriteIDUnresolved)
	expect(t, "PR item that names no repository", rest(t, g, grant, "PATCH", "/orgs/acme/projectsV2/4/items/16", body), contract.GitHubWriteIDUnresolved)
	expect(t, "issue item that names no repository", rest(t, g, grant, "PATCH", "/orgs/acme/projectsV2/4/items/17", body), contract.GitHubWriteIDUnresolved)
	if len(up.sent) != 3 {
		t.Fatalf("reached GitHub: %v", up.sent)
	}
}

// A GraphQL project item's content is the union Issue | PullRequest | DraftIssue, read by its __typename: an issue or a PR in an
// allowed repository passes, one in another repository is refused, a draft or any other type is refused.
func TestGraphQLProjectItemContentIsAnIssueOrPullRequestOfAnAllowedRepository(t *testing.T) {
	g, up := newGuard()
	grant := grantOf("acme/app", contract.GitHubWriteProjects)
	set := func(item string) contract.GitHubWriteReason {
		return graphql(t, g, grant, `mutation($i:UpdateProjectV2ItemFieldValueInput!){updateProjectV2ItemFieldValue(input:$i){clientMutationId}}`, `{"i":{"projectId":"P_ok","itemId":"`+item+`","fieldId":"F","value":{}}}`)
	}
	expect(t, "issue item of the repository", set("IT_ok"), "")
	expect(t, "PR item of the repository", set("IT_pr"), "")
	expect(t, "issue item of another repository", set("IT_other"), contract.GitHubWriteRepoNotAllowed)
	expect(t, "PR item of another repository", set("IT_pr_other"), contract.GitHubWriteRepoNotAllowed)
	expect(t, "draft item", set("IT_draft"), contract.GitHubWriteRepoNotAllowed)
	expect(t, "content of a type that is none of the three", set("IT_unknown"), contract.GitHubWriteRepoNotAllowed)
	if len(up.sent) != 2 {
		t.Fatalf("reached GitHub: %v", up.sent)
	}
}

// createIssue takes ids of a repository's milestone and labels, which must be of an allowed repository like repositoryId; an
// assignee is a user and belongs to none; an issue type belongs to an organization and is not accepted.
func TestCreateIssueIDsAreOfAllowedRepositories(t *testing.T) {
	g, up := newGuard()
	grant := grantOf("acme/app", contract.GitHubWriteIssues)
	const query = `mutation($i:CreateIssueInput!){createIssue(input:$i){issue{id}}}`
	create := func(fields string) contract.GitHubWriteReason {
		return graphql(t, g, grant, query, `{"i":{"repositoryId":"R_ok","title":"x",`+fields+`}}`)
	}
	expect(t, "milestone of the repository", create(`"milestoneId":"M_ok"`), "")
	expect(t, "labels of the repository", create(`"labelIds":["L_ok","L_ok"]`), "")
	expect(t, "no labels", create(`"labelIds":[]`), "")
	expect(t, "assignees", create(`"assigneeIds":["U_anyone"]`), "")
	expect(t, "milestone of another repository", create(`"milestoneId":"M_other"`), contract.GitHubWriteRepoNotAllowed)
	expect(t, "an issue id as the milestone", create(`"milestoneId":"I_ok"`), contract.GitHubWriteRepoNotAllowed)
	expect(t, "a label of another repository", create(`"labelIds":["L_ok","L_other"]`), contract.GitHubWriteRepoNotAllowed)
	expect(t, "a milestone id as a label", create(`"labelIds":["M_ok"]`), contract.GitHubWriteRepoNotAllowed)
	expect(t, "labels that are one string", create(`"labelIds":"L_ok"`), contract.GitHubWriteGraphQLShape)
	expect(t, "an empty label id", create(`"labelIds":[""]`), contract.GitHubWriteGraphQLShape)
	expect(t, "a label that is not a string", create(`"labelIds":[7]`), contract.GitHubWriteGraphQLShape)
	expect(t, "an unknown label", create(`"labelIds":["L_nope"]`), contract.GitHubWriteGraphQLUnresolved)
	expect(t, "an issue type", create(`"issueTypeId":"IT_any"`), contract.GitHubWriteFieldNotAllowed)
	if len(up.sent) != 4 {
		t.Fatalf("reached GitHub: %v", up.sent)
	}
}

func TestOwnedPullLifecycle(t *testing.T) {
	for _, owner := range []bool{true, false} {
		for _, granted := range []bool{true, false} {
			g, _ := newGuard()
			g.Owns = func(ctx context.Context, repo contract.RepoName, number int) (bool, error) {
				if repo != "acme/app" || number != 7 {
					t.Fatalf("untrusted target: %s %d", repo, number)
				}
				return owner, nil
			}
			grant := grantOf("acme/app", contract.GitHubWriteIssues)
			if granted {
				grant.Groups = append(grant.Groups, contract.GitHubWritePulls)
			}
			var want contract.GitHubWriteReason
			if !owner {
				want = "pr_not_owned"
			}
			if !granted {
				want = contract.GitHubWriteGroupOff
			}
			for _, body := range []string{`{"state":"closed"}`, `{"state":"open"}`, `{"base":"release"}`, `{"title":"x","body":"y","base":"release","state":"closed"}`} {
				expect(t, body, rest(t, g, grant, "PATCH", "/repos/acme/app/pulls/7", body), want)
			}
			for _, name := range []string{"closePullRequest", "reopenPullRequest", "updatePullRequest"} {
				input := `{"i":{"pullRequestId":"PR_ok","clientMutationId":"x"}}`
				if name == "updatePullRequest" {
					input = `{"i":{"pullRequestId":"PR_ok","baseRefName":"release","title":"x"}}`
				}
				expect(t, name, graphql(t, g, grant, "mutation($i:X!){"+name+"(input:$i){clientMutationId}}", input), want)
			}
		}
	}
}

func TestPullOwnershipLookupFailsClosed(t *testing.T) {
	sentinel := errors.New("store unavailable")
	for _, graphqlRequest := range []bool{false, true} {
		for _, missing := range []bool{false, true} {
			g, up := newGuard()
			if !missing {
				g.Owns = func(context.Context, contract.RepoName, int) (bool, error) { return false, sentinel }
			}
			body := `{"state":"closed"}`
			method, target := "PATCH", "https://api.github.com/repos/acme/app/pulls/7"
			if graphqlRequest {
				method, target = "POST", "https://api.github.com/graphql"
				body = `{"query":"mutation($i:X!){closePullRequest(input:$i){clientMutationId}}","variables":{"i":{"pullRequestId":"PR_ok"}}}`
			}
			req, _ := http.NewRequestWithContext(WithAllowed(context.Background(), grantOf("acme/app", pulls)), method, target, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			_, err := g.RoundTrip(req)
			if missing {
				var refusal *Refusal
				if !errors.As(err, &refusal) || refusal.Reason != contract.GitHubWritePRNotOwned || refusal.Detail != "only the session that owns this PR, or a session above it, can close, reopen or retarget it" {
					t.Fatalf("missing hook: %v", err)
				}
			} else if !errors.Is(err, sentinel) {
				t.Fatalf("lost store error: %v", err)
			}
			if len(up.sent) != 0 {
				t.Fatalf("write escaped: %v", up.sent)
			}
		}
	}
}

func TestPullLifecycleWithWritesOff(t *testing.T) {
	g, _ := newGuard()
	g.Owns = func(context.Context, contract.RepoName, int) (bool, error) {
		t.Fatal("ownership checked without grant")
		return true, nil
	}
	expect(t, "REST", rest(t, g, contract.GitHubWrites{}, "PATCH", "/repos/acme/app/pulls/7", `{"state":"closed","base":"main"}`), contract.GitHubWriteOff)
	for _, name := range []string{"closePullRequest", "reopenPullRequest", "updatePullRequest"} {
		expect(t, name, graphql(t, g, contract.GitHubWrites{}, "mutation($i:X!){"+name+"(input:$i){clientMutationId}}", `{"i":{"pullRequestId":"PR_ok","baseRefName":"main"}}`), contract.GitHubWriteOff)
	}
}

func TestPullOwnershipUsesContextCaller(t *testing.T) {
	for _, caller := range []contract.SessionID{"owner", "sibling", ""} {
		for _, graph := range []bool{false, true} {
			g, up := newGuard()
			// This is the runtime hook's rule: only the recorded session matches, never a tool argument.
			g.Owns = func(ctx context.Context, _ contract.RepoName, _ int) (bool, error) {
				return SessionFrom(ctx) == "owner", nil
			}
			ctx := WithAllowed(context.Background(), grantOf("acme/app", pulls))
			if caller != "" {
				ctx = WithSession(ctx, caller)
			}
			method, target, body := "PATCH", "https://api.github.com/repos/acme/app/pulls/7", `{"state":"closed"}`
			if graph {
				method, target, body = "POST", "https://api.github.com/graphql", `{"query":"mutation($i:X!){closePullRequest(input:$i){clientMutationId}}","variables":{"session":"owner","i":{"pullRequestId":"PR_ok"}}}`
			}
			request, _ := http.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			response, err := g.RoundTrip(request)
			if caller == "owner" {
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
			} else {
				var refusal *Refusal
				if !errors.As(err, &refusal) || refusal.Reason != contract.GitHubWritePRNotOwned {
					t.Fatalf("caller %q: %v", caller, err)
				}
				if len(up.sent) != 0 {
					t.Fatalf("caller %q escaped: %v", caller, up.sent)
				}
			}
		}
	}
}
