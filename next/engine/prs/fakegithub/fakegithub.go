// Package fakegithub is an httptest GitHub for PR and issue tests: the repository feeds with their ETags and 304s, the one
// GraphQL query, and the REST reads the engine makes. An edit shows on the feeds as GitHub would show it: a label is an event
// with an actor, a push only moves updated_at, and a check result moves nothing, so only the query can see it.
package fakegithub

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
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
	State  string    // reviews only
	At     time.Time // when a review was submitted or a comment made
	Edited time.Time // the last edit of the body, if any
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
	NoChecks       bool   // the rollup holds no check
	RollupHead     string // the commit the rollup is of, when it is not Head, as GitHub can answer for a push it has not caught up with
	Reviews        []Comment
	Comments       []Comment
	Meta           Meta
	Reviewers      []string
}

// Meta is what a PR and an issue share. Body is its text. Reopens counts a close and a reopen the edit makes in one go.
type Meta struct {
	Labels    []string
	Assignees []string
	Milestone string
	Body      string
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

// listed is an item as its feeds show it: when it was opened and last moved.
type listed struct {
	number  int
	created time.Time
	updated time.Time
}

type event struct {
	id     int64
	name   string
	actor  string
	number int
	at     time.Time
	extra  map[string]any
	held   bool
}

type comment struct {
	id      int64
	number  int
	user    string
	body    string
	created time.Time
	updated time.Time
	held    bool
}

// Server counts what it answers. Hold no reference to a Pull across a call: use Edit.
type Server struct {
	*httptest.Server
	t *testing.T

	mu             sync.Mutex
	viewer         string
	limit          int
	remaining      int
	reset          time.Time
	retryAfter     int
	pollInterval   int
	clock          time.Time
	nextID         int64
	holding        bool
	mains          map[contract.RepoName]string
	pulls          map[string]*Pull
	issues         map[string]*Issue
	items          map[contract.RepoName][]*listed
	events         map[contract.RepoName][]*event
	comments       map[contract.RepoName][]*comment
	reviewComments map[contract.RepoName][]*comment
	hidden         map[contract.RepoName]bool
	graphql        int
	rest           int
	notModified    int
	sentETags      []string
	tokens         []string
	sizes          []int
}

func key(repo contract.RepoName, number int) string { return fmt.Sprintf("%s#%d", repo, number) }

// New starts a server with one account, arismoko-like viewer "owner-account", and plenty of quota.
func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{t: t, viewer: "owner-account", limit: 5000, remaining: 5000, reset: time.Unix(1<<31, 0), clock: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), nextID: 1000,
		mains: map[contract.RepoName]string{}, pulls: map[string]*Pull{}, issues: map[string]*Issue{}, items: map[contract.RepoName][]*listed{}, events: map[contract.RepoName][]*event{},
		comments: map[contract.RepoName][]*comment{}, reviewComments: map[contract.RepoName][]*comment{}, hidden: map[contract.RepoName]bool{}}
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

// tick moves GitHub's clock, so every change has its own second.
func (s *Server) tick() time.Time {
	s.clock = s.clock.Add(time.Second)
	return s.clock
}

// Sleep moves GitHub's clock without anything happening, for a feed's time window.
func (s *Server) Sleep(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clock = s.clock.Add(d)
}

func (s *Server) id() int64 {
	s.nextID++
	return s.nextID
}

func (s *Server) open(repo contract.RepoName, number int) {
	at := s.tick()
	s.items[repo] = append(s.items[repo], &listed{number: number, created: at, updated: at})
}

func (s *Server) touch(repo contract.RepoName, number int) {
	at := s.tick()
	for _, item := range s.items[repo] {
		if item.number == number {
			item.updated = at
		}
	}
}

func (s *Server) emit(repo contract.RepoName, number int, name, actor string, extra map[string]any) {
	s.events[repo] = append(s.events[repo], &event{id: s.id(), name: name, actor: actor, number: number, at: s.tick(), extra: extra, held: s.holding})
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
	s.open(p.Repo, p.Number)
	s.commentFeed(p.Repo, p.Number, nil, p.Comments)
}

func clonePull(p *Pull) Pull {
	before := *p
	before.Reviews, before.Comments = slices.Clone(p.Reviews), slices.Clone(p.Comments)
	before.Meta.Labels, before.Meta.Assignees = slices.Clone(p.Meta.Labels), slices.Clone(p.Meta.Assignees)
	before.Reviewers = slices.Clone(p.Reviewers)
	return before
}

// Edit changes a PR under the server's lock, as the owner's account.
func (s *Server) Edit(repo contract.RepoName, number int, change func(*Pull)) {
	s.EditAs("", repo, number, change)
}

// EditAs changes a PR as one login, and the feeds show what moved as that login's.
func (s *Server) EditAs(actor string, repo contract.RepoName, number int, change func(*Pull)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if actor == "" {
		actor = s.viewer
	}
	pull := s.pulls[key(repo, number)]
	before := clonePull(pull)
	change(pull)
	moved := s.metaEvents(repo, number, actor, before.Meta, pull.Meta, before.Title, pull.Title)
	if before.State != pull.State {
		moved = true
		switch pull.State {
		case "MERGED":
			s.emit(repo, number, "merged", actor, nil)
			s.emit(repo, number, "closed", actor, nil)
		case "CLOSED":
			s.emit(repo, number, "closed", actor, nil)
		default:
			s.emit(repo, number, "reopened", actor, nil)
		}
	}
	if before.Draft != pull.Draft {
		moved = true
		s.emit(repo, number, map[bool]string{true: "convert_to_draft", false: "ready_for_review"}[pull.Draft], actor, nil)
	}
	for _, login := range pull.Reviewers {
		if !slices.Contains(before.Reviewers, login) {
			moved = true
			s.emit(repo, number, "review_requested", "github-actions[bot]", map[string]any{"review_requester": user(actor), "requested_reviewer": user(login)})
		}
	}
	for _, login := range before.Reviewers {
		if !slices.Contains(pull.Reviewers, login) {
			moved = true
			s.emit(repo, number, "review_request_removed", "github-actions[bot]", map[string]any{"review_requester": user(actor), "requested_reviewer": user(login)})
		}
	}
	moved = s.commentFeed(repo, number, before.Comments, pull.Comments) || moved
	for _, review := range pull.Reviews[len(before.Reviews):] {
		moved = true
		for _, inline := range review.Inline {
			at := s.tick()
			s.reviewComments[repo] = append(s.reviewComments[repo], &comment{id: inline.ID, number: number, user: inline.Author, body: inline.Body, created: at, updated: at, held: s.holding})
		}
	}
	if moved || before.Head != pull.Head || before.ReviewDecision != pull.ReviewDecision || !slices.Equal(before.LatestReviews, pull.LatestReviews) {
		s.touch(repo, number)
	}
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
	s.open(i.Repo, i.Number)
	s.commentFeed(i.Repo, i.Number, nil, i.Comments)
}

// EditIssue changes an issue under the server's lock, as the owner's account.
func (s *Server) EditIssue(repo contract.RepoName, number int, change func(*Issue)) {
	s.EditIssueAs("", repo, number, change)
}

// EditIssueAs changes an issue as one login, and the feeds show what moved as that login's.
func (s *Server) EditIssueAs(actor string, repo contract.RepoName, number int, change func(*Issue)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if actor == "" {
		actor = s.viewer
	}
	issue := s.issues[key(repo, number)]
	before := *issue
	before.Comments = slices.Clone(issue.Comments)
	before.Meta.Labels, before.Meta.Assignees = slices.Clone(issue.Meta.Labels), slices.Clone(issue.Meta.Assignees)
	change(issue)
	moved := s.metaEvents(repo, number, actor, before.Meta, issue.Meta, before.Title, issue.Title)
	if before.State != issue.State {
		moved = true
		s.emit(repo, number, map[string]string{"OPEN": "reopened", "CLOSED": "closed"}[issue.State], actor, nil)
	}
	if s.commentFeed(repo, number, before.Comments, issue.Comments) || moved {
		s.touch(repo, number)
	}
}

// EditComment rewrites a conversation comment's body, which moves it up the feed and starts nothing new.
func (s *Server) EditComment(repo contract.RepoName, id int64, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.comments[repo] {
		if item.id == id {
			item.body, item.updated = body, s.tick()
		}
	}
}

func user(login string) map[string]string { return map[string]string{"login": login} }

func (s *Server) commentFeed(repo contract.RepoName, number int, before, after []Comment) bool {
	for _, item := range after[len(before):] {
		at := s.tick()
		s.comments[repo] = append(s.comments[repo], &comment{id: item.ID, number: number, user: item.Author, body: item.Body, created: at, updated: at, held: s.holding})
	}
	return len(after) > len(before)
}

// metaEvents shows what a PR and an issue share as events, and reports whether anything moved.
func (s *Server) metaEvents(repo contract.RepoName, number int, actor string, before, after Meta, oldTitle, title string) bool {
	moved := false
	emit := func(name, by string, extra map[string]any) {
		moved = true
		s.emit(repo, number, name, by, extra)
	}
	for _, label := range after.Labels {
		if !slices.Contains(before.Labels, label) {
			emit("labeled", actor, map[string]any{"label": map[string]string{"name": label}})
		}
	}
	for _, label := range before.Labels {
		if !slices.Contains(after.Labels, label) {
			emit("unlabeled", actor, map[string]any{"label": map[string]string{"name": label}})
		}
	}
	// GitHub's actor on an assignment is the one assigned, and the one who did it is the assigner.
	for _, login := range after.Assignees {
		if !slices.Contains(before.Assignees, login) {
			emit("assigned", login, map[string]any{"assignee": user(login), "assigner": user(actor)})
		}
	}
	for _, login := range before.Assignees {
		if !slices.Contains(after.Assignees, login) {
			emit("unassigned", login, map[string]any{"assignee": user(login), "assigner": user(actor)})
		}
	}
	if before.Milestone != after.Milestone {
		if before.Milestone != "" {
			emit("demilestoned", actor, map[string]any{"milestone": map[string]string{"title": before.Milestone}})
		}
		if after.Milestone != "" {
			emit("milestoned", actor, map[string]any{"milestone": map[string]string{"title": after.Milestone}})
		}
	}
	if oldTitle != title {
		emit("renamed", actor, map[string]any{"rename": map[string]string{"from": oldTitle, "to": title}})
	}
	for range after.Reopens - before.Reopens {
		emit("closed", actor, nil)
		emit("reopened", actor, nil)
	}
	return moved || before.Body != after.Body
}

// Mention shows a login mentioned on an item, which is how GitHub tells it: the actor is the login, and nothing says who wrote it.
func (s *Server) Mention(repo contract.RepoName, number int, login string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emit(repo, number, "mentioned", login, nil)
}

// Hold keeps what edits add to the feeds off them until Release, for a feed that shows an item late.
func (s *Server) Hold() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.holding = true
}

// Release shows what Hold kept back.
func (s *Server) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.holding = false
	for _, list := range s.events {
		for _, item := range list {
			item.held = false
		}
	}
	for _, feed := range []map[contract.RepoName][]*comment{s.comments, s.reviewComments} {
		for _, list := range feed {
			for _, item := range list {
				item.held = false
			}
		}
	}
}

// HideRepo makes a repository answer as GitHub does when it has none by that name, or the token cannot see it.
func (s *Server) HideRepo(repo contract.RepoName) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hidden[repo] = true
}

// SetMain moves a repository's default branch. A repository starts on head "base".
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

// SetPollInterval makes the default branch's feed send this X-Poll-Interval, as GitHub's does.
func (s *Server) SetPollInterval(seconds int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pollInterval = seconds
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

var alias = regexp.MustCompile(`pr(\d+):pullRequest\(number:(\d+)\)`)

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
	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(s.limit))
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(s.remaining))
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(s.reset.Unix(), 10))
	if r.Method == http.MethodPost && r.URL.Path == "/graphql" {
		s.serveGraphQL(w, r)
		return
	}
	s.serveREST(w, r)
}

func stamp(at time.Time) string { return at.UTC().Format(time.RFC3339) }

// answer sends one JSON body under an ETag of what it holds, or a 304 for one the client already has.
func (s *Server) answer(w http.ResponseWriter, r *http.Request, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		s.t.Error(err)
		return
	}
	sum := sha256.Sum256(data)
	etag := `W/"` + hex.EncodeToString(sum[:16]) + `"`
	if sent := r.Header.Get("If-None-Match"); sent != "" {
		s.sentETags = append(s.sentETags, sent)
		if sent == etag {
			s.notModified++
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.Header().Set("ETag", etag)
	if _, err := w.Write(data); err != nil {
		s.t.Error(err)
	}
}

// page answers one page of a feed, newest first. The issues feed names a page by an opaque cursor as well, so a client must follow Link.
func (s *Server) page(w http.ResponseWriter, r *http.Request, all []map[string]any, cursor bool) {
	size, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if size == 0 {
		size = 30
	}
	page := 1
	if text := r.URL.Query().Get("page"); text != "" {
		page, _ = strconv.Atoi(text)
		if cursor && r.URL.Query().Get("after") != "opaque" {
			http.Error(w, `{"message":"page needs after"}`, http.StatusBadRequest)
			return
		}
	}
	from, to := min((page-1)*size, len(all)), min(page*size, len(all))
	if to < len(all) {
		next := *r.URL
		next.Scheme, next.Host = "http", r.Host
		query := next.Query()
		query.Set("page", strconv.Itoa(page+1))
		if cursor {
			query.Set("after", "opaque")
		}
		next.RawQuery = query.Encode()
		w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, next.String()))
	}
	s.answer(w, r, all[from:to])
}

func (s *Server) itemNode(repo contract.RepoName, row *listed) map[string]any {
	var node map[string]any
	if pull := s.pulls[key(repo, row.number)]; pull != nil {
		state := "open"
		if pull.State != "OPEN" {
			state = "closed"
		}
		var merged any
		if pull.State == "MERGED" {
			merged = stamp(row.updated)
		}
		node = metaNode(map[string]any{"number": pull.Number, "html_url": s.link(pull), "title": pull.Title, "draft": pull.Draft, "state": state, "user": user(pull.Author), "pull_request": map[string]any{"merged_at": merged}}, pull.Meta)
	} else {
		issue := s.issues[key(repo, row.number)]
		node = metaNode(map[string]any{"number": issue.Number, "html_url": fmt.Sprintf("https://github.com/%s/issues/%d", repo, issue.Number), "title": issue.Title, "state": strings.ToLower(issue.State), "user": user(issue.Author)}, issue.Meta)
	}
	node["repository_url"] = fmt.Sprintf("https://api.github.com/repos/%s", repo)
	node["created_at"], node["updated_at"] = stamp(row.created), stamp(row.updated)
	return node
}

func (s *Server) serveFeed(w http.ResponseWriter, r *http.Request, repo contract.RepoName, feed string) {
	var all []map[string]any
	switch feed {
	case "issues":
		rows := slices.Clone(s.items[repo])
		slices.SortFunc(rows, func(a, b *listed) int {
			return cmp.Or(b.updated.Compare(a.updated), cmp.Compare(b.number, a.number))
		})
		for _, row := range rows {
			all = append(all, s.itemNode(repo, row))
		}
	case "issues/events":
		for _, item := range slices.Backward(s.events[repo]) {
			if item.held {
				continue
			}
			node := map[string]any{"id": item.id, "event": item.name, "actor": user(item.actor), "created_at": stamp(item.at)}
			for name, value := range item.extra {
				node[name] = value
			}
			for _, row := range s.items[repo] {
				if row.number == item.number {
					node["issue"] = s.itemNode(repo, row)
				}
			}
			all = append(all, node)
		}
	default:
		list, base := s.comments[repo], "issues"
		if feed == "pulls/comments" {
			list, base = s.reviewComments[repo], "pulls"
		}
		rows := slices.Clone(list)
		slices.SortStableFunc(rows, func(a, b *comment) int { return cmp.Or(b.updated.Compare(a.updated), cmp.Compare(b.id, a.id)) })
		for _, item := range rows {
			if item.held {
				continue
			}
			pathPart := "issues"
			if s.pulls[key(repo, item.number)] != nil {
				pathPart = "pull"
			}
			node := map[string]any{"id": item.id, "body": item.body, "user": user(item.user), "created_at": stamp(item.created), "updated_at": stamp(item.updated),
				"html_url": fmt.Sprintf("https://github.com/%s/%s/%d#c%d", repo, pathPart, item.number, item.id)}
			link := fmt.Sprintf("https://api.github.com/repos/%s/%s/%d", repo, base, item.number)
			if base == "issues" {
				node["issue_url"] = link
			} else {
				node["pull_request_url"] = link
			}
			all = append(all, node)
		}
	}
	if all == nil {
		all = []map[string]any{}
	}
	s.page(w, r, all, feed == "issues")
}

func (s *Server) serveREST(w http.ResponseWriter, r *http.Request) {
	s.rest++
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "repos" {
		http.NotFound(w, r)
		return
	}
	repo := contract.RepoName(parts[1] + "/" + parts[2])
	if s.hidden[repo] {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		return
	}
	rest := strings.Join(parts[3:], "/")
	switch {
	case rest == "":
		s.answer(w, r, map[string]any{"default_branch": "main"})
	case strings.HasPrefix(rest, "git/ref/heads/"):
		head, ok := s.mains[repo]
		if !ok {
			head = "base"
		}
		if s.pollInterval > 0 {
			w.Header().Set("X-Poll-Interval", strconv.Itoa(s.pollInterval))
		}
		s.answer(w, r, map[string]any{"object": map[string]string{"sha": head}})
	case rest == "issues" || rest == "issues/events" || rest == "issues/comments" || rest == "pulls/comments":
		s.serveFeed(w, r, repo, rest)
	case len(parts) == 5 && parts[3] == "pulls":
		number, err := strconv.Atoi(parts[4])
		pull := s.pulls[key(repo, number)]
		if err != nil || pull == nil {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		state := "open"
		if pull.State != "OPEN" {
			state = "closed"
		}
		s.answer(w, r, metaNode(map[string]any{"number": pull.Number, "html_url": s.link(pull), "title": pull.Title, "draft": pull.Draft, "state": state, "merged": pull.State == "MERGED", "user": user(pull.Author),
			"head": map[string]any{"sha": pull.Head, "repo": map[string]string{"full_name": string(pull.HeadRepo)}}, "base": map[string]any{"repo": map[string]string{"full_name": string(pull.Repo)}}}, pull.Meta))
	case len(parts) == 5 && parts[3] == "issues":
		number, err := strconv.Atoi(parts[4])
		if err != nil {
			http.NotFound(w, r)
			return
		}
		for _, row := range s.items[repo] {
			if row.number == number {
				s.answer(w, r, s.itemNode(repo, row))
				return
			}
		}
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) link(p *Pull) string {
	return fmt.Sprintf("https://github.com/%s/pull/%d", p.Repo, p.Number)
}

// edited answers null for a body never edited.
func edited(at time.Time) any {
	if at.IsZero() {
		return nil
	}
	return at
}

func nodes[T any](items []T, node func(T) map[string]any) map[string]any {
	out := []map[string]any{}
	for _, item := range items {
		out = append(out, node(item))
	}
	return map[string]any{"nodes": out}
}

// metaNode puts the metadata fields in a PR's or an issue's REST body.
func metaNode(node map[string]any, m Meta) map[string]any {
	labels := []map[string]string{}
	for _, name := range m.Labels {
		labels = append(labels, map[string]string{"name": name})
	}
	assignees := []map[string]string{}
	for _, login := range m.Assignees {
		assignees = append(assignees, user(login))
	}
	node["labels"], node["assignees"], node["milestone"], node["body"] = labels, assignees, nil, m.Body
	if m.Milestone != "" {
		node["milestone"] = map[string]string{"title": m.Milestone}
	}
	return node
}

func (s *Server) pullNode(p *Pull) map[string]any {
	link := s.link(p)
	var decision, rollup any
	if p.ReviewDecision != "" {
		decision = p.ReviewDecision
	}
	if p.Rollup != "" {
		checks := nodes(p.FailedChecks, func(name string) map[string]any {
			return map[string]any{"__typename": "CheckRun", "name": name, "conclusion": "FAILURE"}
		})
		if !p.NoChecks {
			checks["nodes"] = append(checks["nodes"].([]map[string]any), map[string]any{"__typename": "CheckRun", "name": "build", "conclusion": "SUCCESS"})
		}
		rollup = map[string]any{"state": p.Rollup, "contexts": checks}
	}
	commit := p.Head
	if p.RollupHead != "" {
		commit = p.RollupHead
	}
	return map[string]any{
		"number": p.Number, "state": p.State, "merged": p.State == "MERGED",
		"headRefOid": p.Head, "headRepository": map[string]string{"nameWithOwner": string(p.HeadRepo)}, "mergeable": p.Mergeable, "mergeStateStatus": p.MergeState, "reviewDecision": decision,
		"latestOpinionatedReviews": nodes(p.LatestReviews, func(state string) map[string]any { return map[string]any{"state": state} }),
		"commits":                  map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"oid": commit, "statusCheckRollup": rollup}}}},
		"reviews": nodes(p.Reviews, func(c Comment) map[string]any {
			return map[string]any{"fullDatabaseId": strconv.FormatInt(c.ID, 10), "state": c.State, "body": c.Body, "url": fmt.Sprintf("%s#%d", link, c.ID), "author": user(c.Author), "submittedAt": c.At, "lastEditedAt": edited(c.Edited)}
		}),
		"comments": nodes(p.Comments, func(c Comment) map[string]any {
			return map[string]any{"body": c.Body, "author": user(c.Author), "createdAt": c.At, "lastEditedAt": edited(c.Edited)}
		}),
	}
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
	if input.Variables == nil {
		if err := json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"viewer": user(s.viewer)}}); err != nil {
			s.t.Error(err)
		}
		return
	}
	repo := contract.RepoName(input.Variables["owner"] + "/" + input.Variables["name"])
	repository := map[string]any{}
	var failures []map[string]any
	asked := alias.FindAllStringSubmatch(input.Query, -1)
	s.sizes = append(s.sizes, len(asked))
	if len(asked) == 0 && strings.Contains(input.Query, "fragment pull") {
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
	var answer any = repository
	if s.hidden[repo] {
		answer, failures = nil, []map[string]any{{"type": "NOT_FOUND", "message": "Could not resolve to a Repository"}}
	}
	body := map[string]any{"data": map[string]any{"viewer": user(s.viewer), "repository": answer}}
	if failures != nil {
		body["errors"] = failures
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.t.Error(err)
	}
}
