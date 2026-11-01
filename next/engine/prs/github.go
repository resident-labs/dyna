// Package prs reads tracked pull requests and issues from GitHub inside the request budget of spec 7.7 and tells their sessions what changed.
package prs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// ErrPaused means GitHub refused for its quota and the wait it asked for has not passed.
var ErrPaused = errors.New("GitHub request budget: paused")

// ErrNotFound means GitHub has no such repository, pull request or issue, or the token cannot see it.
var ErrNotFound = errors.New("not found on GitHub")

// Client reads GitHub with the engine's token. REST and GraphQL have separate quotas.
type Client struct {
	Token     string
	Endpoint  *url.URL
	Transport http.RoundTripper
	Now       func() time.Time

	mu      sync.Mutex
	rest    quota
	graphql quota
	etags   map[string]cachedRead
	viewer  string
}

type cachedRead struct {
	etag string
	body []byte
}

// quota is what GitHub last said. low holds until reset, or until GitHub reports more room. until is a refusal's wait.
type quota struct {
	low   bool
	reset time.Time
	until time.Time
}

// note records a quota reading. Under 20% left counts as low (spec 7.7).
func (q *quota) note(limit, remaining int, reset time.Time) {
	q.low = limit > 0 && remaining*5 < limit
	q.reset = reset
}

func (q *quota) lowAt(now time.Time) bool {
	return q.low && now.Before(q.reset) || now.Before(q.until)
}

// Viewer is the token's GitHub login as of the last poll, which is the owner's account (decision 13).
func (c *Client) Viewer() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.viewer
}

// Low reports whether less than 20% of either quota is left, and when the later one resets.
func (c *Client) Low() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.Now()
	var reset time.Time
	low := false
	for _, q := range []*quota{&c.rest, &c.graphql} {
		if !q.lowAt(now) {
			continue
		}
		low = true
		if q.reset.After(reset) {
			reset = q.reset
		}
		if q.until.After(reset) {
			reset = q.until
		}
	}
	return reset, low
}

// refused notes a rate-limit refusal. It waits as long as GitHub asks, and at least ten minutes when GitHub does not say.
func (c *Client) refused(q *quota, header http.Header) {
	now := c.Now()
	until := now.Add(10 * time.Minute)
	if seconds, err := strconv.Atoi(header.Get("Retry-After")); err == nil {
		until = now.Add(time.Duration(seconds) * time.Second)
	} else if reset, err := strconv.ParseInt(header.Get("X-RateLimit-Reset"), 10, 64); err == nil && time.Unix(reset, 0).After(until) {
		until = time.Unix(reset, 0)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if until.After(q.until) {
		q.until = until
	}
}

func (c *Client) send(ctx context.Context, q *quota, method, path string, input []byte, etag string) (*http.Response, []byte, error) {
	c.mu.Lock()
	paused := c.Now().Before(q.until)
	c.mu.Unlock()
	if paused {
		return nil, nil, ErrPaused
	}
	target := *c.Endpoint
	target.Path = strings.TrimRight(target.Path, "/") + path
	var body io.Reader
	if input != nil {
		body = bytes.NewReader(input)
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.Token)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if etag != "" {
		request.Header.Set("If-None-Match", etag)
	}
	client := http.Client{Transport: c.Transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return nil, nil, err
	}
	if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusTooManyRequests {
		if response.Header.Get("Retry-After") != "" || response.Header.Get("X-RateLimit-Remaining") == "0" {
			c.refused(q, response.Header)
			return nil, nil, ErrPaused
		}
	}
	return response, data, nil
}

// Pull is a PR as one REST read gives it, when a PR is first tracked.
type Pull struct {
	Repo     contract.RepoName
	HeadRepo contract.RepoName
	Number   int
	URL      string
	Title    string
	Author   string
	Draft    bool
	State    contract.PrState
	Head     string
}

// Pull reads one PR. It sends the ETag of its last answer, and a 304 does not count against the quota.
func (c *Client) Pull(ctx context.Context, repo contract.RepoName, number int) (Pull, error) {
	if !ValidRepo(repo) || number <= 0 {
		return Pull{}, ErrNotFound
	}
	path := fmt.Sprintf("/repos/%s/pulls/%d", repo, number)
	c.mu.Lock()
	cached := c.etags[path]
	c.mu.Unlock()
	response, data, err := c.send(ctx, &c.rest, http.MethodGet, path, nil, cached.etag)
	if err != nil {
		return Pull{}, err
	}
	limit, limitErr := strconv.Atoi(response.Header.Get("X-RateLimit-Limit"))
	remaining, remainingErr := strconv.Atoi(response.Header.Get("X-RateLimit-Remaining"))
	reset, resetErr := strconv.ParseInt(response.Header.Get("X-RateLimit-Reset"), 10, 64)
	if limitErr == nil && remainingErr == nil && resetErr == nil {
		c.mu.Lock()
		c.rest.note(limit, remaining, time.Unix(reset, 0))
		c.mu.Unlock()
	}
	switch response.StatusCode {
	case http.StatusNotModified:
		data = cached.body
	case http.StatusOK:
		c.mu.Lock()
		if c.etags == nil {
			c.etags = map[string]cachedRead{}
		}
		if etag := response.Header.Get("ETag"); etag != "" {
			c.etags[path] = cachedRead{etag: etag, body: data}
		}
		c.mu.Unlock()
	case http.StatusNotFound, http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect:
		return Pull{}, ErrNotFound
	default:
		return Pull{}, fmt.Errorf("GitHub HTTP %d", response.StatusCode)
	}
	var payload struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		Title   string `json:"title"`
		Draft   bool   `json:"draft"`
		State   string `json:"state"`
		Merged  bool   `json:"merged"`
		User    *struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			SHA  string `json:"sha"`
			Repo *struct {
				FullName contract.RepoName `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
		Base struct {
			Repo struct {
				FullName contract.RepoName `json:"full_name"`
			} `json:"repo"`
		} `json:"base"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return Pull{}, fmt.Errorf("GitHub answer: %w", err)
	}
	if payload.Number != number || !ValidRepo(payload.Base.Repo.FullName) {
		return Pull{}, errors.New("GitHub answer has no such pull request")
	}
	pull := Pull{Repo: payload.Base.Repo.FullName, Number: payload.Number, URL: payload.HTMLURL, Title: payload.Title, Author: ghost, Draft: payload.Draft, State: contract.PrOpen, Head: payload.Head.SHA}
	if payload.User != nil {
		pull.Author = payload.User.Login
	}
	if payload.Head.Repo != nil {
		pull.HeadRepo = payload.Head.Repo.FullName
	}
	if payload.Merged {
		pull.State = contract.PrMerged
	} else if payload.State == "closed" {
		pull.State = contract.PrClosed
	}
	return pull, nil
}

// ghost is GitHub's name for an account that no longer exists.
const ghost = "ghost"

// ValidRepo reports whether repo is one owner/name pair, safe in a REST path.
func ValidRepo(repo contract.RepoName) bool {
	owner, name, ok := strings.Cut(string(repo), "/")
	return ok && validPart(owner) && validPart(name)
}

func validPart(part string) bool {
	if part == "" || part == "." || part == ".." {
		return false
	}
	for _, r := range part {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

// Comment is one review, inline comment or conversation comment on a PR.
type Comment struct {
	ID     int64
	Author string
	Body   string
	URL    string
}

// Observed is one PR as one poll read it. An empty Merge means GitHub has not worked it out.
type Observed struct {
	Number         int
	URL            string
	Title          string
	Author         string
	Draft          bool
	State          contract.PrState
	Head           string
	HeadRepo       contract.RepoName
	Review         contract.PrReview
	CI             contract.PrCI
	Merge          contract.PrMerge
	FailedChecks   []string
	Reviews        []Comment
	ReviewComments []Comment
	Comments       []Comment
	Meta           store.ItemMeta
}

// ObservedIssue is one issue as one poll read it. Its title is in Meta.
type ObservedIssue struct {
	Number   int
	URL      string
	Author   string
	State    contract.IssueState
	Comments []Comment
	Meta     store.ItemMeta
}

// Snapshot is one poll of one repository. Repo is the name GitHub gives it. A number missing from PRs or Issues is one GitHub no longer has.
type Snapshot struct {
	Repo   contract.RepoName
	Viewer string
	Main   string
	PRs    map[int]Observed
	Issues map[int]ObservedIssue
}

// metaFields is the metadata a poll reads on PRs and issues alike. lastEditedAt says a body changed without reading it, and
// the reopen count says an item came back between two polls, which its state cannot.
const metaFields = `labels(first:100){nodes{name}} assignees(first:20){nodes{login}} milestone{title} lastEditedAt timelineItems(itemTypes:[REOPENED_EVENT],first:0){totalCount}`

// commentFields is what a poll reads of a conversation.
const commentFields = `comments(last:10){nodes{fullDatabaseId body url author{login}}}`

// pullFields is everything a poll reads per PR.
const pullFields = `number url title isDraft state merged author{login} headRefOid headRepository{nameWithOwner} mergeable mergeStateStatus reviewDecision
latestOpinionatedReviews(first:20,writersOnly:true){nodes{state}}
commits(last:1){nodes{commit{statusCheckRollup{state contexts(first:50){nodes{__typename ...on CheckRun{name conclusion} ...on StatusContext{context state}}}}}}}
reviews(last:10){nodes{fullDatabaseId state body url author{login} comments(last:20){nodes{fullDatabaseId body url author{login}}}}}
reviewRequests(first:20){nodes{requestedReviewer{__typename ...on User{login} ...on Bot{login} ...on Mannequin{login} ...on Team{combinedSlug}}}}
` + commentFields + ` ` + metaFields

// issueFields is everything a poll reads per issue.
const issueFields = `number url title state author{login} ` + commentFields + ` ` + metaFields

type gqlAuthor struct {
	Login string `json:"login"`
}

type gqlComment struct {
	ID     string     `json:"fullDatabaseId"`
	State  string     `json:"state"`
	Body   string     `json:"body"`
	URL    string     `json:"url"`
	Author *gqlAuthor `json:"author"`
}

type gqlMeta struct {
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Assignees struct {
		Nodes []gqlAuthor `json:"nodes"`
	} `json:"assignees"`
	Milestone *struct {
		Title string `json:"title"`
	} `json:"milestone"`
	LastEditedAt  string `json:"lastEditedAt"`
	TimelineItems struct {
		TotalCount int `json:"totalCount"`
	} `json:"timelineItems"`
}

// meta sorts the names, because GitHub's order means nothing and a change in it is no change.
func (m gqlMeta) meta(title string) store.ItemMeta {
	meta := store.ItemMeta{Title: title, EditedAt: m.LastEditedAt, Reopened: m.TimelineItems.TotalCount}
	for _, label := range m.Labels.Nodes {
		meta.Labels = append(meta.Labels, label.Name)
	}
	for _, assignee := range m.Assignees.Nodes {
		meta.Assignees = append(meta.Assignees, assignee.Login)
	}
	if m.Milestone != nil {
		meta.Milestone = m.Milestone.Title
	}
	slices.Sort(meta.Labels)
	slices.Sort(meta.Assignees)
	return meta
}

type gqlIssue struct {
	gqlMeta
	Number   int        `json:"number"`
	URL      string     `json:"url"`
	Title    string     `json:"title"`
	State    string     `json:"state"`
	Author   *gqlAuthor `json:"author"`
	Comments struct {
		Nodes []gqlComment `json:"nodes"`
	} `json:"comments"`
}

func (i gqlIssue) observed() ObservedIssue {
	seen := ObservedIssue{Number: i.Number, URL: i.URL, Author: login(i.Author), State: contract.IssueOpen, Meta: i.meta(i.Title)}
	if i.State == "CLOSED" {
		seen.State = contract.IssueClosed
	}
	for _, node := range i.Comments.Nodes {
		seen.Comments = append(seen.Comments, comment(node))
	}
	return seen
}

type gqlPull struct {
	gqlMeta
	Number         int        `json:"number"`
	URL            string     `json:"url"`
	Title          string     `json:"title"`
	IsDraft        bool       `json:"isDraft"`
	State          string     `json:"state"`
	Merged         bool       `json:"merged"`
	Author         *gqlAuthor `json:"author"`
	HeadRefOid     string     `json:"headRefOid"`
	HeadRepository *struct {
		NameWithOwner contract.RepoName `json:"nameWithOwner"`
	} `json:"headRepository"`
	Mergeable                string `json:"mergeable"`
	MergeStateStatus         string `json:"mergeStateStatus"`
	ReviewDecision           string `json:"reviewDecision"`
	LatestOpinionatedReviews struct {
		Nodes []struct {
			State string `json:"state"`
		} `json:"nodes"`
	} `json:"latestOpinionatedReviews"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					State    string `json:"state"`
					Contexts struct {
						Nodes []struct {
							Name       string `json:"name"`
							Conclusion string `json:"conclusion"`
							Context    string `json:"context"`
							State      string `json:"state"`
						} `json:"nodes"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	Reviews struct {
		Nodes []struct {
			gqlComment
			Comments struct {
				Nodes []gqlComment `json:"nodes"`
			} `json:"comments"`
		} `json:"nodes"`
	} `json:"reviews"`
	Comments struct {
		Nodes []gqlComment `json:"nodes"`
	} `json:"comments"`
	ReviewRequests struct {
		Nodes []struct {
			RequestedReviewer *struct {
				Login        string `json:"login"`
				CombinedSlug string `json:"combinedSlug"`
			} `json:"requestedReviewer"`
		} `json:"nodes"`
	} `json:"reviewRequests"`
}

// Poll reads every given PR and issue and the default branch head in one GraphQL query (spec 7.7). A failure to find one
// leaves the others usable, as GitHub answers such a query with partial data.
func (c *Client) Poll(ctx context.Context, repo contract.RepoName, numbers, issues []int) (Snapshot, error) {
	owner, name, ok := strings.Cut(string(repo), "/")
	if !ok || !ValidRepo(repo) {
		return Snapshot{}, fmt.Errorf("invalid repository %q", repo)
	}
	var fields strings.Builder
	for _, number := range numbers {
		fmt.Fprintf(&fields, "pr%d:pullRequest(number:%d){...pull}\n", number, number)
	}
	for _, number := range issues {
		fmt.Fprintf(&fields, "issue%d:issue(number:%d){...issue}\n", number, number)
	}
	query := "query($owner:String!,$name:String!){viewer{login} rateLimit{limit remaining resetAt} repository(owner:$owner,name:$name){nameWithOwner defaultBranchRef{target{oid}}\n" + fields.String() + "}}"
	// GitHub refuses a fragment nothing uses, and a repository with nothing tracked asks only for its head.
	if len(numbers) > 0 {
		query += "\nfragment pull on PullRequest{" + pullFields + "}"
	}
	if len(issues) > 0 {
		query += "\nfragment issue on Issue{" + issueFields + "}"
	}
	input, err := json.Marshal(map[string]any{"query": query, "variables": map[string]string{"owner": owner, "name": name}})
	if err != nil {
		return Snapshot{}, err
	}
	response, data, err := c.send(ctx, &c.graphql, http.MethodPost, "/graphql", input, "")
	if err != nil {
		return Snapshot{}, err
	}
	if response.StatusCode != http.StatusOK {
		return Snapshot{}, fmt.Errorf("GitHub HTTP %d", response.StatusCode)
	}
	var payload struct {
		Data struct {
			Viewer    gqlAuthor `json:"viewer"`
			RateLimit *struct {
				Limit     int       `json:"limit"`
				Remaining int       `json:"remaining"`
				ResetAt   time.Time `json:"resetAt"`
			} `json:"rateLimit"`
			Repository map[string]json.RawMessage `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return Snapshot{}, fmt.Errorf("GitHub answer: %w", err)
	}
	c.mu.Lock()
	if rate := payload.Data.RateLimit; rate != nil {
		c.graphql.note(rate.Limit, rate.Remaining, rate.ResetAt)
	}
	if payload.Data.Viewer.Login != "" {
		c.viewer = payload.Data.Viewer.Login
	}
	c.mu.Unlock()
	for _, failure := range payload.Errors {
		if failure.Type == "RATE_LIMITED" {
			c.refused(&c.graphql, response.Header)
			return Snapshot{}, ErrPaused
		}
	}
	if payload.Data.Repository == nil {
		message := "repository not readable"
		if len(payload.Errors) > 0 {
			message = strings.ReplaceAll(payload.Errors[0].Message, c.Token, "***")
			if payload.Errors[0].Type == "NOT_FOUND" {
				return Snapshot{}, fmt.Errorf("%w: %s", ErrNotFound, message)
			}
		}
		return Snapshot{}, fmt.Errorf("GitHub: %s", message)
	}
	snapshot := Snapshot{Repo: repo, Viewer: payload.Data.Viewer.Login, PRs: make(map[int]Observed, len(numbers)), Issues: make(map[int]ObservedIssue, len(issues))}
	if raw := payload.Data.Repository["nameWithOwner"]; raw != nil {
		if err := json.Unmarshal(raw, &snapshot.Repo); err != nil {
			return Snapshot{}, fmt.Errorf("GitHub answer: %w", err)
		}
	}
	var branch *struct {
		Target struct {
			Oid string `json:"oid"`
		} `json:"target"`
	}
	if raw := payload.Data.Repository["defaultBranchRef"]; raw != nil {
		if err := json.Unmarshal(raw, &branch); err != nil {
			return Snapshot{}, fmt.Errorf("GitHub answer: %w", err)
		}
	}
	if branch != nil {
		snapshot.Main = branch.Target.Oid
	}
	for _, number := range numbers {
		raw := payload.Data.Repository[fmt.Sprintf("pr%d", number)]
		if raw == nil || string(raw) == "null" {
			continue
		}
		var pull gqlPull
		if err := json.Unmarshal(raw, &pull); err != nil {
			return Snapshot{}, fmt.Errorf("GitHub answer: %w", err)
		}
		snapshot.PRs[number] = pull.observed()
	}
	for _, number := range issues {
		raw := payload.Data.Repository[fmt.Sprintf("issue%d", number)]
		if raw == nil || string(raw) == "null" {
			continue
		}
		var issue gqlIssue
		if err := json.Unmarshal(raw, &issue); err != nil {
			return Snapshot{}, fmt.Errorf("GitHub answer: %w", err)
		}
		snapshot.Issues[number] = issue.observed()
	}
	return snapshot, nil
}

// Issue reads one issue when it is first tracked, in the query the poll uses, so tracking adds no REST read. It returns the
// repository's name as GitHub gives it. GitHub has no issue at a PR's number.
func (c *Client) Issue(ctx context.Context, repo contract.RepoName, number int) (contract.RepoName, ObservedIssue, error) {
	if !ValidRepo(repo) || number <= 0 {
		return "", ObservedIssue{}, ErrNotFound
	}
	snapshot, err := c.Poll(ctx, repo, nil, []int{number})
	if err != nil {
		return "", ObservedIssue{}, err
	}
	issue, found := snapshot.Issues[number]
	if !found {
		return "", ObservedIssue{}, ErrNotFound
	}
	return snapshot.Repo, issue, nil
}

func login(author *gqlAuthor) string {
	if author == nil {
		return ghost
	}
	return author.Login
}

func comment(node gqlComment) Comment {
	// GitHub's IDs are decimal strings. A malformed one reads as 0, which is never newer than a cursor.
	id, _ := strconv.ParseInt(node.ID, 10, 64)
	return Comment{ID: id, Author: login(node.Author), Body: node.Body, URL: node.URL}
}

func (p gqlPull) observed() Observed {
	seen := Observed{Number: p.Number, URL: p.URL, Title: p.Title, Author: login(p.Author), Draft: p.IsDraft, State: contract.PrOpen, Head: p.HeadRefOid, CI: contract.PrCINone}
	if p.Merged {
		seen.State = contract.PrMerged
	} else if p.State == "CLOSED" {
		seen.State = contract.PrClosed
	}
	if p.HeadRepository != nil {
		seen.HeadRepo = p.HeadRepository.NameWithOwner
	}
	switch {
	case p.Mergeable == "CONFLICTING" || p.MergeStateStatus == "DIRTY":
		seen.Merge = contract.PrMergeConflict
	case p.Mergeable == "MERGEABLE" && p.MergeStateStatus == "BLOCKED":
		seen.Merge = contract.PrMergeBlocked
	case p.Mergeable == "MERGEABLE" && p.MergeStateStatus == "BEHIND":
		seen.Merge = contract.PrMergeBehind
	case p.Mergeable == "MERGEABLE":
		seen.Merge = contract.PrMergeClean
	}
	// reviewDecision is null on a branch that requires no review, so the latest reviews from people with write access decide there.
	switch p.ReviewDecision {
	case "APPROVED":
		seen.Review = contract.PrApproved
	case "CHANGES_REQUESTED":
		seen.Review = contract.PrChangesRequested
	case "REVIEW_REQUIRED":
		seen.Review = contract.PrReviewRequired
	default:
		for _, review := range p.LatestOpinionatedReviews.Nodes {
			if review.State == "CHANGES_REQUESTED" {
				seen.Review = contract.PrChangesRequested
				break
			}
			if review.State == "APPROVED" {
				seen.Review = contract.PrApproved
			}
		}
	}
	if len(p.Commits.Nodes) > 0 && p.Commits.Nodes[0].Commit.StatusCheckRollup != nil {
		rollup := p.Commits.Nodes[0].Commit.StatusCheckRollup
		switch rollup.State {
		case "SUCCESS":
			seen.CI = contract.PrCISuccess
		case "FAILURE", "ERROR":
			seen.CI = contract.PrCIFailure
		default:
			seen.CI = contract.PrCIPending
		}
		for _, check := range rollup.Contexts.Nodes {
			switch {
			case check.Conclusion == "FAILURE" || check.Conclusion == "TIMED_OUT" || check.Conclusion == "STARTUP_FAILURE" || check.Conclusion == "CANCELLED":
				seen.FailedChecks = append(seen.FailedChecks, check.Name)
			case check.State == "FAILURE" || check.State == "ERROR":
				seen.FailedChecks = append(seen.FailedChecks, check.Context)
			}
		}
	}
	for _, review := range p.Reviews.Nodes {
		if review.State == "PENDING" {
			continue
		}
		seen.Reviews = append(seen.Reviews, comment(review.gqlComment))
		for _, inline := range review.Comments.Nodes {
			seen.ReviewComments = append(seen.ReviewComments, comment(inline))
		}
	}
	for _, node := range p.Comments.Nodes {
		seen.Comments = append(seen.Comments, comment(node))
	}
	seen.Meta = p.meta(p.Title)
	seen.Meta.Draft = p.IsDraft
	for _, node := range p.ReviewRequests.Nodes {
		if reviewer := node.RequestedReviewer; reviewer != nil {
			seen.Meta.Reviewers = append(seen.Meta.Reviewers, reviewer.Login+reviewer.CombinedSlug)
		}
	}
	slices.Sort(seen.Meta.Reviewers)
	return seen
}
