package view

import (
	"bytes"
	"context"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/pr/state"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// gh's issue JSON omits PR state and draft status. These queries fetch all
// issue rows and their linked PRs together through the same gh process.
const issueFields = `number title state stateReason createdAt updatedAt closedAt url
 author { login __typename ... on User { name } }
 labels(first:100) { nodes { name color } }
 assignees(first:20) { nodes { login } }
 commentCount: comments { totalCount }
 closedByPullRequestsReferences(first:100, includeClosedPrs:true) { nodes { number state isDraft url } }`

const issueListQuery = `query IssueList($owner:String!, $repo:String!, $search:String!) {
 repository(owner:$owner,name:$repo) { nameWithOwner url }
 search(query:$search,type:ISSUE,first:60) { pageInfo { hasNextPage } nodes { ... on Issue { ` + issueFields + ` } } }
}`

const issueViewQuery = `query IssueView($owner:String!, $repo:String!, $number:Int!, $endCursor:String) {
 repository(owner:$owner,name:$repo) { issue(number:$number) { ` + issueFields + ` body
 comments(first:100, after:$endCursor) { pageInfo { hasNextPage endCursor } nodes { author { login __typename ... on User { name } } body createdAt url } }
 } }
}`

// flattenIssue turns GraphQL connections into the arrays used by gh's JSON.
func flattenIssue(raw json.RawMessage) ([]byte, error) {
	var item map[string]json.RawMessage
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, err
	}
	if item == nil {
		return nil, fmt.Errorf("GitHub issue was not found")
	}
	// The total is a plain number in the row, as gh's own JSON has it.
	if len(item["commentCount"]) > 0 {
		var count struct {
			TotalCount json.RawMessage `json:"totalCount"`
		}
		if err := json.Unmarshal(item["commentCount"], &count); err != nil {
			return nil, err
		}
		item["commentCount"] = count.TotalCount
	}
	for _, field := range []string{"labels", "assignees", "comments", "closedByPullRequestsReferences"} {
		if len(item[field]) == 0 {
			continue
		}
		var connection struct {
			Nodes json.RawMessage `json:"nodes"`
		}
		if err := json.Unmarshal(item[field], &connection); err != nil {
			return nil, err
		}
		item[field] = connection.Nodes
	}
	return json.Marshal(item)
}

func issueQuery(ctx context.Context, root, query string, fields ...string) ([]byte, error) {
	repo, err := readGitHubRepository(ctx, root)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(repo.URL)
	owner, name, ok := strings.Cut(repo.NameWithOwner, "/")
	if err != nil || u.Host == "" || !ok || owner == "" || name == "" {
		return nil, fmt.Errorf("gh returned an invalid repository")
	}
	args := []string{"api", "graphql", "--hostname", u.Host, "-f", "query=" + query, "-f", "owner=" + owner, "-f", "repo=" + name}
	if query == issueViewQuery {
		args = append(args, "--paginate")
	}
	for _, field := range fields {
		flag := "-f"
		if strings.HasPrefix(field, "number=") {
			flag = "-F"
		}
		args = append(args, flag, strings.NewReplacer("{owner}", owner, "{repo}", name).Replace(field))
	}
	return runGH(ctx, root, args...)
}

func (s *Service) githubIssueList(ctx context.Context, root, roomRef, state, search string) (GitHubListResult, error) {
	filter := "repo:{owner}/{repo} is:issue sort:created-desc"
	if state != "all" {
		filter += " is:" + state
	}
	if search = strings.TrimSpace(search); search != "" {
		filter += " " + search
	}
	b, err := issueQuery(ctx, root, issueListQuery, "search="+filter)
	if err != nil {
		return GitHubListResult{}, err
	}
	var result struct {
		Data struct {
			Repository struct {
				NameWithOwner string `json:"nameWithOwner"`
				URL           string `json:"url"`
			} `json:"repository"`
			Search struct {
				Nodes    []json.RawMessage `json:"nodes"`
				PageInfo struct {
					HasNextPage bool `json:"hasNextPage"`
				} `json:"pageInfo"`
			} `json:"search"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		return GitHubListResult{}, fmt.Errorf("read GitHub issue list: %w", err)
	}
	out := GitHubListResult{Account: ghclient.Account(ctx, "", root), Repo: result.Data.Repository.NameWithOwner, URL: result.Data.Repository.URL, Kind: "issue", State: state, More: result.Data.Search.PageInfo.HasNextPage, Items: []GitHubItem{}}
	for _, raw := range result.Data.Search.Nodes {
		flat, err := flattenIssue(raw)
		if err != nil {
			return GitHubListResult{}, err
		}
		var item ghItem
		if err := json.Unmarshal(flat, &item); err != nil {
			return GitHubListResult{}, err
		}
		out.Items = append(out.Items, item.item())
	}
	if err := s.issueWork(roomRef, out.Items); err != nil {
		return GitHubListResult{}, err
	}
	return out, nil
}

func (s *Service) githubIssueView(ctx context.Context, root, roomRef string, number int) (GitHubDetail, error) {
	b, err := issueQuery(ctx, root, issueViewQuery, "number="+strconv.Itoa(number))
	if err != nil {
		return GitHubDetail{}, err
	}
	var out GitHubDetail
	decoder := json.NewDecoder(bytes.NewReader(b))
	pages := 0
	for {
		var result struct {
			Data struct {
				Repository struct {
					Issue json.RawMessage `json:"issue"`
				} `json:"repository"`
			} `json:"data"`
		}
		if err := decoder.Decode(&result); err != nil {
			if err == io.EOF {
				break
			}
			return GitHubDetail{}, fmt.Errorf("read GitHub issue page: %w", err)
		}
		flat, err := flattenIssue(result.Data.Repository.Issue)
		if err != nil {
			return GitHubDetail{}, err
		}
		page, err := parseGitHubDetail("issue", flat)
		if err != nil {
			return GitHubDetail{}, err
		}
		if pages == 0 {
			out = page
		} else {
			out.Comments = append(out.Comments, page.Comments...)
		}
		pages++
	}
	if pages == 0 {
		return GitHubDetail{}, fmt.Errorf("GitHub returned no issue pages")
	}
	items := []GitHubItem{out.GitHubItem}
	err = s.issueWork(roomRef, items)
	out.GitHubItem = items[0]
	return out, err
}

// githubName is a safe GitHub owner or repository name, so it can sit
// inside a GraphQL string.
var githubName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// workedIssueQueries builds one GraphQL query per GitHub host that reads every
// worked issue by number: repository r<i>, issue i<n>. Work records hold
// lowercased host/owner/repo names, which GitHub matches without case.
func workedIssueQueries(work []state.IssueWork) map[string]string {
	byRepo := map[string][]int{}
	for _, w := range work {
		byRepo[w.Repo] = append(byRepo[w.Repo], w.Number)
	}
	repos := make([]string, 0, len(byRepo))
	for repo := range byRepo {
		repos = append(repos, repo)
	}
	slices.Sort(repos)
	bodies := map[string]*strings.Builder{}
	i := 0
	for _, repo := range repos {
		parts := strings.Split(repo, "/")
		if len(parts) != 3 || !githubName.MatchString(parts[1]) || !githubName.MatchString(parts[2]) {
			continue
		}
		b := bodies[parts[0]]
		if b == nil {
			b = &strings.Builder{}
			bodies[parts[0]] = b
		}
		fmt.Fprintf(b, " r%d: repository(owner:%q,name:%q) {", i, parts[1], parts[2])
		numbers := byRepo[repo]
		slices.Sort(numbers)
		for _, n := range slices.Compact(numbers) {
			fmt.Fprintf(b, " i%d: issue(number:%d) { %s }", n, n, issueFields)
		}
		b.WriteString(" }")
		i++
	}
	out := make(map[string]string, len(bodies))
	for host, b := range bodies {
		out[host] = "query {" + b.String() + " }"
	}
	return out
}

// parseWorkedIssues reads a workedIssueQueries answer. A missing issue, such
// as a deleted one, is left out.
func parseWorkedIssues(b []byte) ([]GitHubItem, error) {
	var result struct {
		Data map[string]map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		return nil, fmt.Errorf("read GitHub worked issues: %w", err)
	}
	var items []GitHubItem
	for _, repo := range result.Data {
		for _, raw := range repo {
			if string(raw) == "null" {
				continue
			}
			flat, err := flattenIssue(raw)
			if err != nil {
				return nil, err
			}
			var item ghItem
			if err := json.Unmarshal(flat, &item); err != nil {
				return nil, err
			}
			items = append(items, item.item())
		}
	}
	slices.SortFunc(items, func(a, b GitHubItem) int { return strings.Compare(a.URL, b.URL) })
	return items, nil
}

// workedIssues reads the GitHub facts of issues with live room work.
func (s *Service) workedIssues(roomRef string, work []state.IssueWork) ([]GitHubItem, error) {
	root, err := s.WorkspaceRoot(roomRef, "")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), ghTimeout)
	defer cancel()
	ctx = ghclient.WithRoom(ghclient.WithRooms(ctx, s.Notify.Rooms), roomRef)
	var items []GitHubItem
	for host, query := range workedIssueQueries(work) {
		b, err := runGH(ctx, root, "api", "graphql", "--hostname", host, "-f", "query="+query)
		if err != nil {
			return items, err
		}
		found, err := parseWorkedIssues(b)
		if err != nil {
			return items, err
		}
		items = append(items, found...)
	}
	return items, nil
}
