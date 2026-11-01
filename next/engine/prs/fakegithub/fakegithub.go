// Package fakegithub is an httptest GitHub for PR and issue tests: the one GraphQL query and the one REST read the engine makes.
package fakegithub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

const Token = "fake-github-token"

// Comment is a review, an inline comment or a conversation comment.
type Comment struct {
	ID     int64
	Author string
	Body   string
	State  string // reviews only
	Inline []Comment
}

// Pull is one PR. Empty ReviewDecision and Rollup answer null, as GitHub does.
type Pull struct {
	Repo           contract.RepoName
	Number         int
	Title          string
	Author         string
	Draft          bool
	State          string // OPEN, CLOSED or MERGED
	Head           string
	HeadRepo       contract.RepoName
	Mergeable      string
	MergeState     string
	ReviewDecision string
	LatestReviews  []string
	Rollup         string
	FailedChecks   []string
	Reviews        []Comment
	Comments       []Comment
	Meta           Meta
	Reviewers      []string
}

// Meta is what a PR and an issue share. An empty EditedAt answers null, as for a body nobody edited. Reopens is GitHub's count of reopen events.
type Meta struct {
	Labels    []string
	Assignees []string
	Milestone string
	EditedAt  string
	Reopens   int
}

// Issue is one issue.
type Issue struct {
	Repo     contract.RepoName
	Number   int
	Title    string
	Author   string
	State    string // OPEN or CLOSED
	Comments []Comment
	Meta     Meta
}

// Server counts what it answers. Hold no reference to a Pull across a call: use Edit.
type Server struct {
	*httptest.Server
	t *testing.T

	mu          sync.Mutex
	viewer      string
	limit       int
	remaining   int
	reset       time.Time
	retryAfter  int
	mains       map[contract.RepoName]string
	pulls       map[string]*Pull
	issues      map[string]*Issue
	hidden      map[contract.RepoName]bool
	graphql     int
	rest        int
	notModified int
	sentETags   []string
	tokens      []string
	sizes       []int
	issueSizes  []int
}

func key(repo contract.RepoName, number int) string { return fmt.Sprintf("%s#%d", repo, number) }

// New starts a server with one account, arismoko-like viewer "owner-account", and plenty of quota.
func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{t: t, viewer: "owner-account", limit: 5000, remaining: 5000, reset: time.Unix(1<<31, 0), mains: map[contract.RepoName]string{}, pulls: map[string]*Pull{}, issues: map[string]*Issue{}, hidden: map[contract.RepoName]bool{}}
	s.Server = httptest.NewServer(s)
	t.Cleanup(s.Close)
	return s
}

// Endpoint is the address to give prs.Client.
func (s *Server) Endpoint() *url.URL {
	endpoint, err := url.Parse(s.URL)
	if err != nil {
		s.t.Fatal(err)
	}
	return endpoint
}

// Add creates a PR that is open, mergeable and without checks or reviews, unless p says otherwise.
func (s *Server) Add(p Pull) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.State == "" {
		p.State = "OPEN"
	}
	if p.Author == "" {
		p.Author = s.viewer
	}
	if p.Head == "" {
		p.Head = "head1"
	}
	if p.HeadRepo == "" {
		p.HeadRepo = p.Repo
	}
	if p.Mergeable == "" {
		p.Mergeable, p.MergeState = "MERGEABLE", "CLEAN"
	}
	if p.Title == "" {
		p.Title = fmt.Sprintf("PR %d", p.Number)
	}
	s.pulls[key(p.Repo, p.Number)] = &p
}

// Edit changes a PR under the server's lock.
func (s *Server) Edit(repo contract.RepoName, number int, change func(*Pull)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change(s.pulls[key(repo, number)])
}

// AddIssue creates an open issue, unless i says otherwise.
func (s *Server) AddIssue(i Issue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i.State == "" {
		i.State = "OPEN"
	}
	if i.Author == "" {
		i.Author = s.viewer
	}
	if i.Title == "" {
		i.Title = fmt.Sprintf("Issue %d", i.Number)
	}
	s.issues[key(i.Repo, i.Number)] = &i
}

// EditIssue changes an issue under the server's lock.
func (s *Server) EditIssue(repo contract.RepoName, number int, change func(*Issue)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change(s.issues[key(repo, number)])
}

// HideRepo makes a repository answer as GitHub does when it has none by that name, or the token cannot see it.
func (s *Server) HideRepo(repo contract.RepoName) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hidden[repo] = true
}

// SetMain moves a repository's default branch.
func (s *Server) SetMain(repo contract.RepoName, head string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mains[repo] = head
}

// SetQuota sets what both rate limits report.
func (s *Server) SetQuota(limit, remaining int, reset time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limit, s.remaining, s.reset = limit, remaining, reset
}

// RefuseFor answers every request 429 with this Retry-After. Zero stops.
func (s *Server) RefuseFor(seconds int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retryAfter = seconds
}

// Counts is what the server has answered: GraphQL queries, REST reads, and the REST reads that were 304.
func (s *Server) Counts() (graphql, rest, notModified int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.graphql, s.rest, s.notModified
}

// SentETags lists each If-None-Match it received.
func (s *Server) SentETags() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sentETags...)
}

// QuerySizes lists how many PRs each GraphQL query asked for.
func (s *Server) QuerySizes() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.sizes...)
}

// IssueQuerySizes lists how many issues each GraphQL query asked for.
func (s *Server) IssueQuerySizes() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.issueSizes...)
}

var alias = regexp.MustCompile(`pr(\d+):pullRequest\(number:(\d+)\)`)
var issueAlias = regexp.MustCompile(`issue(\d+):issue\(number:(\d+)\)`)

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+Token {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		return
	}
	if s.retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(s.retryAfter))
		http.Error(w, `{"message":"slow down"}`, http.StatusTooManyRequests)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/graphql" {
		s.serveGraphQL(w, r)
		return
	}
	s.serveREST(w, r)
}

func (s *Server) serveREST(w http.ResponseWriter, r *http.Request) {
	s.rest++
	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(s.limit))
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(s.remaining))
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(s.reset.Unix(), 10))
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 5 || parts[0] != "repos" || parts[3] != "pulls" {
		http.NotFound(w, r)
		return
	}
	number, err := strconv.Atoi(parts[4])
	pull := s.pulls[key(contract.RepoName(parts[1]+"/"+parts[2]), number)]
	if err != nil || pull == nil {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		return
	}
	etag := fmt.Sprintf(`"%s-%s-%s"`, pull.Head, pull.State, pull.Title)
	if sent := r.Header.Get("If-None-Match"); sent != "" {
		s.sentETags = append(s.sentETags, sent)
		if sent == etag {
			s.notModified++
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	state := "open"
	if pull.State != "OPEN" {
		state = "closed"
	}
	w.Header().Set("ETag", etag)
	body := map[string]any{"number": pull.Number, "html_url": s.link(pull), "title": pull.Title, "draft": pull.Draft, "state": state, "merged": pull.State == "MERGED", "user": map[string]string{"login": pull.Author},
		"head": map[string]any{"sha": pull.Head, "repo": map[string]string{"full_name": string(pull.HeadRepo)}}, "base": map[string]any{"repo": map[string]string{"full_name": string(pull.Repo)}}}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.t.Error(err)
	}
}

func (s *Server) link(p *Pull) string {
	return fmt.Sprintf("https://github.com/%s/pull/%d", p.Repo, p.Number)
}

func nodes[T any](items []T, node func(T) map[string]any) map[string]any {
	out := []map[string]any{}
	for _, item := range items {
		out = append(out, node(item))
	}
	return map[string]any{"nodes": out}
}

func commentNode(url string) func(Comment) map[string]any {
	return func(c Comment) map[string]any {
		return map[string]any{"fullDatabaseId": strconv.FormatInt(c.ID, 10), "body": c.Body, "url": fmt.Sprintf("%s#%d", url, c.ID), "author": map[string]string{"login": c.Author}}
	}
}

// metaNode puts the metadata fields in a PR's or an issue's node.
func metaNode(node map[string]any, m Meta) map[string]any {
	node["labels"] = nodes(m.Labels, func(name string) map[string]any { return map[string]any{"name": name} })
	node["assignees"] = nodes(m.Assignees, func(login string) map[string]any { return map[string]any{"login": login} })
	node["milestone"], node["lastEditedAt"] = nil, nil
	if m.Milestone != "" {
		node["milestone"] = map[string]string{"title": m.Milestone}
	}
	if m.EditedAt != "" {
		node["lastEditedAt"] = m.EditedAt
	}
	node["timelineItems"] = map[string]int{"totalCount": m.Reopens}
	return node
}

func (s *Server) issueNode(i *Issue) map[string]any {
	link := fmt.Sprintf("https://github.com/%s/issues/%d", i.Repo, i.Number)
	return metaNode(map[string]any{"number": i.Number, "url": link, "title": i.Title, "state": i.State, "author": map[string]string{"login": i.Author}, "comments": nodes(i.Comments, commentNode(link))}, i.Meta)
}

func (s *Server) pullNode(p *Pull) map[string]any {
	link := s.link(p)
	var decision, rollup any
	if p.ReviewDecision != "" {
		decision = p.ReviewDecision
	}
	if p.Rollup != "" {
		rollup = map[string]any{"state": p.Rollup, "contexts": nodes(p.FailedChecks, func(name string) map[string]any {
			return map[string]any{"__typename": "CheckRun", "name": name, "conclusion": "FAILURE"}
		})}
	}
	return metaNode(map[string]any{
		"number": p.Number, "url": link, "title": p.Title, "isDraft": p.Draft, "state": p.State, "merged": p.State == "MERGED", "author": map[string]string{"login": p.Author},
		"headRefOid": p.Head, "headRepository": map[string]string{"nameWithOwner": string(p.HeadRepo)}, "mergeable": p.Mergeable, "mergeStateStatus": p.MergeState, "reviewDecision": decision,
		"latestOpinionatedReviews": nodes(p.LatestReviews, func(state string) map[string]any { return map[string]any{"state": state} }),
		"commits":                  map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": rollup}}}},
		"reviews": nodes(p.Reviews, func(c Comment) map[string]any {
			node := commentNode(link)(c)
			node["state"] = c.State
			node["comments"] = nodes(c.Inline, commentNode(link))
			return node
		}),
		"comments": nodes(p.Comments, commentNode(link)),
		"reviewRequests": nodes(p.Reviewers, func(login string) map[string]any {
			return map[string]any{"requestedReviewer": map[string]string{"__typename": "User", "login": login}}
		}),
	}, p.Meta)
}

func (s *Server) serveGraphQL(w http.ResponseWriter, r *http.Request) {
	s.graphql++
	var input struct {
		Query     string            `json:"query"`
		Variables map[string]string `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	repo := contract.RepoName(input.Variables["owner"] + "/" + input.Variables["name"])
	repository := map[string]any{"nameWithOwner": string(repo), "defaultBranchRef": nil}
	if head, ok := s.mains[repo]; ok {
		repository["defaultBranchRef"] = map[string]any{"target": map[string]string{"oid": head}}
	}
	var failures []map[string]any
	asked := alias.FindAllStringSubmatch(input.Query, -1)
	askedIssues := issueAlias.FindAllStringSubmatch(input.Query, -1)
	s.sizes = append(s.sizes, len(asked))
	s.issueSizes = append(s.issueSizes, len(askedIssues))
	if len(asked) == 0 && strings.Contains(input.Query, "fragment pull") || len(askedIssues) == 0 && strings.Contains(input.Query, "fragment issue") {
		if err := json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"type": "undefinedFragment", "message": "Fragment was defined, but not used"}}}); err != nil {
			s.t.Error(err)
		}
		return
	}
	for _, match := range asked {
		number, err := strconv.Atoi(match[2])
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if pull := s.pulls[key(repo, number)]; pull != nil {
			repository["pr"+match[1]] = s.pullNode(pull)
		} else {
			repository["pr"+match[1]] = nil
			failures = append(failures, map[string]any{"type": "NOT_FOUND", "message": "Could not resolve to a PullRequest"})
		}
	}
	for _, match := range askedIssues {
		number, err := strconv.Atoi(match[2])
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if issue := s.issues[key(repo, number)]; issue != nil {
			repository["issue"+match[1]] = s.issueNode(issue)
		} else {
			repository["issue"+match[1]] = nil
			failures = append(failures, map[string]any{"type": "NOT_FOUND", "message": "Could not resolve to an Issue"})
		}
	}
	var answer any = repository
	if s.hidden[repo] {
		answer, failures = nil, []map[string]any{{"type": "NOT_FOUND", "message": "Could not resolve to a Repository"}}
	}
	body := map[string]any{"data": map[string]any{"viewer": map[string]string{"login": s.viewer}, "rateLimit": map[string]any{"limit": s.limit, "remaining": s.remaining, "resetAt": s.reset.UTC().Format(time.RFC3339)}, "repository": answer}}
	if failures != nil {
		body["errors"] = failures
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.t.Error(err)
	}
}
