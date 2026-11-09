package githubwrites

import (
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

// route is one REST method and path a group allows. A segment is a literal, or {owner} and {repo}, which name what the grant is
// checked against, or {number}, which is digits. Fields, when set, is the exact list of keys the JSON object body may have.
type route struct {
	method  string
	path    string
	group   contract.GitHubWriteGroup
	fields  []string
	project bool // {owner} owns a project and no repository, so the grant is checked by owner
	notPR   bool // with no field list, a PATCH is allowed on an issue and, beyond labels, not on a PR (the issues API reaches a PR by its number)
	comment bool // {number} names a comment, and the comment's issue must be in the allowed repository of the path
	ownEdit bool // lifecycle fields require the PR owner
	item    bool // the last {number} names a project item, and the item's content must be in an allowed repository
}

// routes is the allow-list of REST writes. A write that matches none of these is refused, whatever it is.
var routes = []route{
	{method: http.MethodPost, path: "repos/{owner}/{repo}/actions/workflows/{workflow}/dispatches", group: contract.GitHubWriteActions},
	{method: http.MethodPost, path: "repos/{owner}/{repo}/actions/runs/{number}/rerun", group: contract.GitHubWriteActions},
	{method: http.MethodPost, path: "repos/{owner}/{repo}/actions/runs/{number}/rerun-failed-jobs", group: contract.GitHubWriteActions},
	{method: http.MethodPost, path: "repos/{owner}/{repo}/actions/runs/{number}/cancel", group: contract.GitHubWriteActions},
	{method: http.MethodPost, path: "repos/{owner}/{repo}/issues", group: issues},
	{method: http.MethodPatch, path: "repos/{owner}/{repo}/issues/{number}", group: issues, notPR: true},
	{method: http.MethodPost, path: "repos/{owner}/{repo}/issues/{number}/comments", group: issues},
	{method: http.MethodPatch, path: "repos/{owner}/{repo}/issues/comments/{number}", group: issues, comment: true},
	{method: http.MethodPost, path: "repos/{owner}/{repo}/issues/{number}/labels", group: issues},
	{method: http.MethodPut, path: "repos/{owner}/{repo}/issues/{number}/labels", group: issues},

	{method: http.MethodPatch, path: "repos/{owner}/{repo}/pulls/{number}", group: pulls, ownEdit: true, fields: []string{"title", "body", "state", "base"}},
	{method: http.MethodPatch, path: "repos/{owner}/{repo}/issues/{number}", group: pulls, fields: []string{"labels"}},
	{method: http.MethodPost, path: "repos/{owner}/{repo}/issues/{number}/labels", group: pulls},
	{method: http.MethodPut, path: "repos/{owner}/{repo}/issues/{number}/labels", group: pulls},
	{method: http.MethodPost, path: "repos/{owner}/{repo}/issues/{number}/comments", group: pulls},
	{method: http.MethodPatch, path: "repos/{owner}/{repo}/issues/comments/{number}", group: pulls, comment: true},
	{method: http.MethodPost, path: "repos/{owner}/{repo}/pulls/{number}/comments", group: pulls},

	{method: http.MethodPatch, path: "orgs/{owner}/projectsV2/{number}/items/{number}", group: projects, fields: []string{"fields"}, project: true, item: true},
	{method: http.MethodPatch, path: "users/{owner}/projectsV2/{number}/items/{number}", group: projects, fields: []string{"fields"}, project: true, item: true},
}

var (
	namePart   = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	numberPart = regexp.MustCompile(`^[0-9]{1,18}$`)
)

// matched is the owner and repository a route's path names.
type matched struct{ owner, repo, number string }

// match reads a path against the route: its segments, whole, with no empty or dot segment.
func (r route) match(segments []string) (matched, bool) {
	pattern := strings.Split(r.path, "/")
	if len(pattern) != len(segments) {
		return matched{}, false
	}
	var found matched
	for i, want := range pattern {
		switch want {
		case "{owner}":
			found.owner = segments[i]
		case "{workflow}":
		case "{repo}":
			found.repo = segments[i]
		case "{number}":
			if !numberPart.MatchString(segments[i]) {
				return matched{}, false
			}
			if found.number == "" {
				found.number = segments[i]
			}
			continue
		default:
			if segments[i] != want {
				return matched{}, false
			}
			continue
		}
		if !namePart.MatchString(segments[i]) || segments[i] == "." || segments[i] == ".." {
			return matched{}, false
		}
	}
	return found, true
}

// segmentsOf splits a request path under the API's own prefix, or says it is not a path of one canonical form: percent-encoding
// that changes the path, an empty segment, a trailing slash or a dot segment.
func segmentsOf(request *http.Request, prefix string) ([]string, bool) {
	url := request.URL
	if url.Opaque != "" || url.RawPath != "" || url.RawQuery != "" || url.ForceQuery || url.Fragment != "" {
		return nil, false
	}
	rest, ok := strings.CutPrefix(url.Path, prefix+"/")
	if !ok || rest == "" {
		return nil, false
	}
	segments := strings.Split(rest, "/")
	if slices.Contains(segments, "") || slices.Contains(segments, ".") || slices.Contains(segments, "..") {
		return nil, false
	}
	return segments, true
}

// checkREST allows a REST write that matches a route of a group the grant has on, on a repository (or project owner) it allows,
// with body keys the route lists. body is the request's JSON, read once by the caller.
func (g *Guard) checkREST(grant contract.GitHubWrites, request *http.Request, prefix string, body []byte) error {
	what := request.Method + " " + request.URL.Path
	segments, canonical := segmentsOf(request, prefix)
	if !canonical {
		return refuse(contract.GitHubWriteRouteNotAllowed, "%s is not a path GitHub writes are allowed on: it has a query, an empty or dot segment, a trailing slash or percent-encoding that changes it", what)
	}
	var onRoute, enabled []route
	var found matched
	for _, each := range routes {
		if each.method != request.Method {
			continue
		}
		if m, ok := each.match(segments); ok {
			onRoute, found = append(onRoute, each), m
			if grant.Has(each.group) {
				enabled = append(enabled, each)
			}
		}
	}
	if len(onRoute) == 0 {
		return refuse(contract.GitHubWriteRouteNotAllowed, "%s is not among the GitHub writes a room can allow", what)
	}
	if len(enabled) == 0 {
		return refuse(contract.GitHubWriteGroupOff, "%s belongs to a write group this room has not turned on", what)
	}
	if enabled[0].project {
		if !grant.AllowsOwner(found.owner) {
			return refuse(contract.GitHubWriteRepoNotAllowed, "%s is a project of %s, which owns none of the room's allowed repositories", what, found.owner)
		}
	} else if !grant.Allows(found.owner + "/" + found.repo) {
		return refuse(contract.GitHubWriteRepoNotAllowed, "%s is on %s/%s, which is not among the repositories this room allows", what, found.owner, found.repo)
	}
	if enabled[0].group == contract.GitHubWriteActions {
		return g.checkActions(request, found, segments, prefix, body)
	}
	if enabled[0].comment || enabled[0].item {
		var answer struct {
			IssueURL    string `json:"issue_url"`
			ContentType string `json:"content_type"`
			Content     *struct {
				RepositoryURL string `json:"repository_url"`
				URL           string `json:"url"`
			} `json:"content"`
		}
		target := *g.API
		target.Path = prefix + "/" + strings.Join(segments, "/")
		if g.ask(request, http.MethodGet, target.String(), nil, &answer) != nil {
			return refuse(contract.GitHubWriteIDUnresolved, "%s names its target by a number, and GitHub did not answer for it", what)
		}
		pointed := answer.IssueURL
		if enabled[0].item {
			switch {
			case answer.ContentType == "DraftIssue":
				return refuse(contract.GitHubWriteRepoNotAllowed, "%s is an item with no issue or PR, so no allowed repository holds it", what)
			case answer.Content != nil && answer.ContentType == "Issue":
				pointed = answer.Content.RepositoryURL
			case answer.Content != nil && answer.ContentType == "PullRequest":
				pointed = answer.Content.URL
			default:
				return refuse(contract.GitHubWriteIDUnresolved, "%s is an item whose content type is neither an issue, a PR nor a draft", what)
			}
		}
		named, ok := strings.CutPrefix(pointed, g.API.Scheme+"://"+g.API.Host+prefix+"/repos/")
		parts := strings.Split(named, "/")
		if !ok || len(parts) < 2 {
			return refuse(contract.GitHubWriteIDUnresolved, "%s names its target by a number, and GitHub's answer names no repository of the API host", what)
		}
		if !grant.Allows(parts[0] + "/" + parts[1]) {
			return refuse(contract.GitHubWriteRepoNotAllowed, "%s names a target in %s/%s, which is not among the repositories this room allows", what, parts[0], parts[1])
		}
	}
	if !json.Valid(body) {
		return refuse(contract.GitHubWriteBodyUnreadable, "the body of %s is not one JSON value", what)
	}
	var keys map[string]json.RawMessage
	asObject := json.Unmarshal(body, &keys) == nil
	for _, each := range enabled {
		if each.fields == nil {
			if each.notPR && (!asObject || !keysWithin(keys, []string{"labels"})) && g.isPullRequest(request, found) {
				return refuse(contract.GitHubWriteFieldNotAllowed, "%s is a PR, and the issues group edits nothing of a PR but its labels", what)
			}
			return nil
		}
		if asObject && keysWithin(keys, each.fields) {
			if each.ownEdit {
				state, hasState := keys["state"]
				_, hasBase := keys["base"]
				if hasState {
					var value string
					if json.Unmarshal(state, &value) != nil || (value != "open" && value != "closed") {
						return refuse(contract.GitHubWriteFieldNotAllowed, "a PR state is open or closed")
					}
				}
				if hasState || hasBase {
					number, err := strconv.Atoi(found.number)
					if err != nil {
						return err
					}
					return g.requireOwner(request.Context(), contract.RepoName(found.owner+"/"+found.repo), number)
				}
			}
			return nil
		}
	}
	return refuse(contract.GitHubWriteFieldNotAllowed, "the body of %s sets a field outside the ones this room allows here (%s)", what, strings.Join(enabled[0].fields, ", "))
}

// keysWithin says whether every key of an object is in the list, compared exactly.
func keysWithin(keys map[string]json.RawMessage, list []string) bool {
	for key := range keys {
		if !slices.Contains(list, key) {
			return false
		}
	}
	return true
}
