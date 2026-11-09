// Package prs watches GitHub's repository feeds inside the request budget of spec 7.7 and tells sessions what changed on their pull requests and issues.
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
	"regexp"
	"sort"
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
	others  map[string]*quota // the quotas only agents' reads spend, such as search, by GitHub's resource name
	etags   map[string]cachedRead
	viewer  string
	resting map[string]time.Time // when a feed that asked for a longer X-Poll-Interval may be read again
}

type cachedRead struct {
	etag string
	body []byte
}

// quota is what GitHub last said. low holds until reset, or until GitHub reports more room. until is a refusal's wait.
type quota struct {
	low              bool
	limit, remaining int
	reset            time.Time
	until            time.Time
}

// note records a quota reading. Under 20% left counts as low (spec 7.7).
func (q *quota) note(limit, remaining int, reset time.Time) {
	q.low = limit > 0 && remaining*5 < limit
	q.limit, q.remaining, q.reset = limit, remaining, reset
}

func (q *quota) lowAt(now time.Time) bool {
	return q.low && now.Before(q.reset) || now.Before(q.until)
}

// Viewer is the token's GitHub login, which is the owner's account (decision 13).
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
	resource, query, _ := strings.Cut(path, "?")
	target.Path, target.RawQuery = strings.TrimRight(target.Path, "/")+resource, query
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
	limit, limitErr := strconv.Atoi(response.Header.Get("X-RateLimit-Limit"))
	remaining, remainingErr := strconv.Atoi(response.Header.Get("X-RateLimit-Remaining"))
	reset, resetErr := strconv.ParseInt(response.Header.Get("X-RateLimit-Reset"), 10, 64)
	if limitErr == nil && remainingErr == nil && resetErr == nil {
		c.mu.Lock()
		q.note(limit, remaining, time.Unix(reset, 0))
		c.mu.Unlock()
	}
	if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusTooManyRequests {
		if response.Header.Get("Retry-After") != "" || response.Header.Get("X-RateLimit-Remaining") == "0" {
			c.refused(q, response.Header)
			return nil, nil, ErrPaused
		}
	}
	return response, data, nil
}

// get reads one REST path. A 304 returns no body, and does not count against the quota.
func (c *Client) get(ctx context.Context, path, etag string) (http.Header, []byte, error) {
	response, data, err := c.send(ctx, &c.rest, http.MethodGet, path, nil, etag)
	if err != nil {
		return nil, nil, err
	}
	switch response.StatusCode {
	case http.StatusOK:
		return response.Header, data, nil
	case http.StatusNotModified:
		return response.Header, nil, nil
	case http.StatusNotFound, http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect:
		return nil, nil, ErrNotFound
	}
	return nil, nil, fmt.Errorf("GitHub HTTP %d", response.StatusCode)
}

// cached reads one REST path, sending the ETag of its last answer.
func (c *Client) cached(ctx context.Context, path string) ([]byte, error) {
	c.mu.Lock()
	last := c.etags[path]
	c.mu.Unlock()
	header, data, err := c.get(ctx, path, last.etag)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return last.body, nil
	}
	if etag := header.Get("ETag"); etag != "" {
		c.mu.Lock()
		if c.etags == nil {
			c.etags = map[string]cachedRead{}
		}
		c.etags[path] = cachedRead{etag: etag, body: data}
		c.mu.Unlock()
	}
	return data, nil
}

// Pull is a PR as one REST read gives it, when a PR is first tracked.
type Pull struct {
	Repo       contract.RepoName
	HeadRepo   contract.RepoName
	HeadBranch string // GitHub head.ref, not the commit SHA in Head.
	Number     int
	URL        string
	Title      string
	Author     string
	Draft      bool
	State      contract.PrState
	Head       string
	Meta       store.ItemMeta
}

// Pull reads one PR.
func (c *Client) Pull(ctx context.Context, repo contract.RepoName, number int) (Pull, error) {
	if !ValidRepo(repo) || number <= 0 {
		return Pull{}, ErrNotFound
	}
	data, err := c.cached(ctx, fmt.Sprintf("/repos/%s/pulls/%d", repo, number))
	if err != nil {
		return Pull{}, err
	}
	var payload struct {
		restItem
		Merged bool `json:"merged"`
		Head   struct {
			SHA  string `json:"sha"`
			Ref  string `json:"ref"`
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
	item := payload.feedItem()
	pull := Pull{Repo: payload.Base.Repo.FullName, Number: number, URL: item.URL, Title: item.Title, Author: item.Author, Draft: item.Draft, State: contract.PrOpen, Head: payload.Head.SHA, HeadBranch: payload.Head.Ref, Meta: item.Meta}
	if payload.Head.Repo != nil {
		pull.HeadRepo = payload.Head.Repo.FullName
	}
	if payload.Merged {
		pull.State = contract.PrMerged
	} else if item.State == "closed" {
		pull.State = contract.PrClosed
	}
	return pull, nil
}

// Issue reads one issue when it is first tracked, and returns the repository's name as GitHub gives it. A PR's number is no issue.
func (c *Client) Issue(ctx context.Context, repo contract.RepoName, number int) (contract.RepoName, FeedItem, error) {
	if !ValidRepo(repo) || number <= 0 {
		return "", FeedItem{}, ErrNotFound
	}
	data, err := c.cached(ctx, fmt.Sprintf("/repos/%s/issues/%d", repo, number))
	if err != nil {
		return "", FeedItem{}, err
	}
	var item restItem
	if err := json.Unmarshal(data, &item); err != nil {
		return "", FeedItem{}, fmt.Errorf("GitHub answer: %w", err)
	}
	if item.Number != number || item.PullRequest != nil {
		return "", FeedItem{}, ErrNotFound
	}
	if _, name, ok := strings.Cut(item.RepositoryURL, "/repos/"); ok && ValidRepo(contract.RepoName(name)) {
		repo = contract.RepoName(name)
	}
	return repo, item.feedItem(), nil
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

// Review is one submitted review, which no repository feed shows.
type Review struct {
	ID     int64
	Author string
	Body   string
	URL    string
}

// Observed is what a feed cannot show of one PR. An empty Merge means GitHub has not worked it out.
type Observed struct {
	Number       int
	State        contract.PrState
	Head         string
	HeadRepo     contract.RepoName
	Review       contract.PrReview
	CI           contract.PrCI
	Merge        contract.PrMerge
	FailedChecks []string
	Reviews      []Review
}

// pullFields is everything a read asks per PR.
const pullFields = `number state merged headRefOid headRepository{nameWithOwner} mergeable mergeStateStatus reviewDecision
latestOpinionatedReviews(first:20,writersOnly:true){nodes{state}}
commits(last:1){nodes{commit{oid ` + suiteFields + ` statusCheckRollup{state contexts(first:50){totalCount pageInfo{hasNextPage} nodes{__typename ...on CheckRun{name status conclusion} ...on StatusContext{context state}}}}}}}
reviews(last:20){nodes{fullDatabaseId state body url author{login} submittedAt lastEditedAt}}
comments(last:20){nodes{body author{login} createdAt lastEditedAt}}`

type gqlAuthor struct {
	Login string `json:"login"`
}

// gqlRollup is a commit's check rollup: GitHub's own state and each check run or status in it.
type gqlRollup struct {
	State    string `json:"state"`
	Contexts struct {
		TotalCount int `json:"totalCount"`
		PageInfo   struct {
			HasNextPage bool `json:"hasNextPage"`
		} `json:"pageInfo"`
		Nodes []struct {
			Type       string `json:"__typename"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			DetailsURL string `json:"detailsUrl"`
			Context    string `json:"context"`
			State      string `json:"state"`
			TargetURL  string `json:"targetUrl"`
		} `json:"nodes"`
	} `json:"contexts"`
}

// suiteFields includes the run count without listing runs: a needs-gated run may
// not exist yet even when every currently visible context has finished.
const suiteFields = "checkSuites(first:100){totalCount pageInfo{hasNextPage} nodes{status conclusion app{slug} createdAt checkRuns{totalCount}}}"

type gqlSuites struct {
	TotalCount int `json:"totalCount"`
	PageInfo   struct {
		HasNextPage bool `json:"hasNextPage"`
	} `json:"pageInfo"`
	Nodes []struct {
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		App        struct {
			Slug string `json:"slug"`
		} `json:"app"`
		CreatedAt time.Time `json:"createdAt"`
		CheckRuns struct {
			TotalCount int `json:"totalCount"`
		} `json:"checkRuns"`
	} `json:"nodes"`
}

// summarizeCI is shared by tracked PRs and main heads. Never settle from a
// partial list, or while a suite can still create needs-gated runs. GitHub apps
// sometimes leave ghost suites queued with no runs; only those older than ten
// minutes can be ignored. Suite conclusions do not replace context semantics.
func summarizeCI(rollup *gqlRollup, suites gqlSuites, rule ciRule, now time.Time) (contract.PrCI, []string) {
	if suites.PageInfo.HasNextPage || suites.TotalCount > len(suites.Nodes) {
		return contract.PrCIPending, nil
	}
	for _, suite := range suites.Nodes {
		if suite.Status != "COMPLETED" && !(suite.CheckRuns.TotalCount == 0 && !suite.CreatedAt.IsZero() && now.Sub(suite.CreatedAt) > 10*time.Minute) {
			return contract.PrCIPending, nil
		}
	}
	if rollup != nil && (rollup.Contexts.PageInfo.HasNextPage || rollup.Contexts.TotalCount > len(rollup.Contexts.Nodes)) {
		return contract.PrCIPending, nil
	}
	// A rollup settles only when every context is terminal. Cancellation without
	// failure is cancelled, not success; an empty success means no checks ran.
	if rollup == nil || rollup.State == "SUCCESS" && len(rollup.Contexts.Nodes) == 0 {
		return contract.PrCINone, nil
	}
	var failed []string
	if len(rollup.Contexts.Nodes) > 0 {
		ci, cancelled := contract.PrCISuccess, []string{}
		for _, check := range rollup.Contexts.Nodes {
			name := check.Name
			if check.Type == "StatusContext" {
				name = check.Context
			}
			switch {
			case check.Type == "StatusContext" && (check.State == "PENDING" || check.State == "EXPECTED"), check.Type != "StatusContext" && check.Status != "COMPLETED":
				return contract.PrCIPending, nil
			case check.Type == "StatusContext" && check.State == "SUCCESS", check.Conclusion == "SUCCESS" || check.Conclusion == "SKIPPED" || check.Conclusion == "NEUTRAL":
			case check.Conclusion == "CANCELLED":
				cancelled = append(cancelled, name)
			default:
				ci = contract.PrCIFailure
				failed = append(failed, name)
			}
		}
		if ci != contract.PrCIFailure && len(cancelled) > 0 {
			ci, failed = contract.PrCICancelled, cancelled
		}
		return ci, failed
	}
	if rule == ciMainHead {
		return contract.PrCIPending, nil
	}
	ci := contract.PrCIPending
	switch rollup.State {
	case "FAILURE", "ERROR":
		ci = contract.PrCIFailure
	}
	return ci, failed
}

// ciRule preserves the PR/main distinction for empty rollups. summarizeCI first
// holds incomplete suites and truncated context lists pending.
type ciRule int

const (
	// ciMainHead keeps an empty non-success rollup pending until the head leaves the recent-head list.
	ciMainHead ciRule = iota
	// ciPullHead follows GitHub's state for an empty non-success rollup.
	ciPullHead
)

type gqlPull struct {
	Number         int    `json:"number"`
	State          string `json:"state"`
	Merged         bool   `json:"merged"`
	HeadRefOid     string `json:"headRefOid"`
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
				OID               string     `json:"oid"`
				CheckSuites       gqlSuites  `json:"checkSuites"`
				StatusCheckRollup *gqlRollup `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	Reviews struct {
		Nodes []struct {
			ID     string     `json:"fullDatabaseId"`
			State  string     `json:"state"`
			Body   string     `json:"body"`
			URL    string     `json:"url"`
			Author *gqlAuthor `json:"author"`
			// SubmittedAt is null for a pending review. LastEditedAt is null for a body never edited.
			SubmittedAt  *time.Time `json:"submittedAt"`
			LastEditedAt *time.Time `json:"lastEditedAt"`
		} `json:"nodes"`
	} `json:"reviews"`
	Comments struct {
		Nodes []struct {
			Body         string     `json:"body"`
			Author       *gqlAuthor `json:"author"`
			CreatedAt    time.Time  `json:"createdAt"`
			LastEditedAt *time.Time `json:"lastEditedAt"`
		} `json:"nodes"`
	} `json:"comments"`
}

// verdictLine is the approval convention for a PR opened by the account that reviews it, which GitHub cannot record as a
// review: a line in a comment, or in the body of a review, that names the sha it judged.
var verdictLine = regexp.MustCompile(`\A[^\n]*\n\*\*Verdict: (Approved|Changes requested) at ([0-9a-f]{12,40})\*\*[ \t]*(?:\n|$)`)

// graphQL runs one query and returns its repository, which is nil for a query that names none.
func (c *Client) graphQL(ctx context.Context, query string, variables map[string]string) (map[string]json.RawMessage, error) {
	input, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return nil, err
	}
	response, data, err := c.send(ctx, &c.graphql, http.MethodPost, "/graphql", input, "")
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub HTTP %d", response.StatusCode)
	}
	var payload struct {
		Data struct {
			Viewer     gqlAuthor                  `json:"viewer"`
			Repository map[string]json.RawMessage `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("GitHub answer: %w", err)
	}
	if payload.Data.Viewer.Login != "" {
		c.mu.Lock()
		c.viewer = payload.Data.Viewer.Login
		c.mu.Unlock()
	}
	for _, failure := range payload.Errors {
		if failure.Type == "RATE_LIMITED" {
			c.refused(&c.graphql, response.Header)
			return nil, ErrPaused
		}
	}
	if payload.Data.Repository == nil && variables != nil {
		message := "repository not readable"
		if len(payload.Errors) > 0 {
			message = strings.ReplaceAll(payload.Errors[0].Message, c.Token, "***")
			if payload.Errors[0].Type == "NOT_FOUND" {
				return nil, fmt.Errorf("%w: %s", ErrNotFound, message)
			}
		}
		return nil, fmt.Errorf("GitHub: %s", message)
	}
	return payload.Data.Repository, nil
}

// LoadViewer asks who the token is, once, because a quiet pass makes no query that would say.
func (c *Client) LoadViewer(ctx context.Context) error {
	if c.Viewer() != "" {
		return nil
	}
	_, err := c.graphQL(ctx, "query{viewer{login}}", nil)
	return err
}

// Read asks for what the feeds cannot show of each given PR, in one query for the repository. A PR it cannot find is left out,
// as GitHub answers such a query with partial data.
func (c *Client) Read(ctx context.Context, repo contract.RepoName, numbers []int) (map[int]*Observed, error) {
	if len(numbers) == 0 {
		return nil, nil
	}
	owner, name, ok := strings.Cut(string(repo), "/")
	if !ok || !ValidRepo(repo) {
		return nil, fmt.Errorf("invalid repository %q", repo)
	}
	var fields strings.Builder
	for _, number := range numbers {
		fmt.Fprintf(&fields, "pr%d:pullRequest(number:%d){...pull}\n", number, number)
	}
	query := "query($owner:String!,$name:String!){viewer{login} repository(owner:$owner,name:$name){\n" + fields.String() + "}}\nfragment pull on PullRequest{" + pullFields + "}"
	repository, err := c.graphQL(ctx, query, map[string]string{"owner": owner, "name": name})
	if err != nil {
		return nil, err
	}
	pulls := make(map[int]*Observed, len(numbers))
	for _, number := range numbers {
		raw := repository[fmt.Sprintf("pr%d", number)]
		if raw == nil || string(raw) == "null" {
			continue
		}
		var pull gqlPull
		if err := json.Unmarshal(raw, &pull); err != nil {
			return nil, fmt.Errorf("GitHub answer: %w", err)
		}
		seen := pull.observed(c.Viewer(), c.Now())
		pulls[number] = &seen
	}
	return pulls, nil
}

func login(author *gqlAuthor) string {
	if author == nil {
		return ghost
	}
	return author.Login
}

// observed reads one PR's answer. viewer is the token's account, whose marked comments and review bodies can carry the review (see verdictLine).
func (p gqlPull) observed(viewer string, now time.Time) Observed {
	seen := Observed{Number: p.Number, State: contract.PrOpen, Head: p.HeadRefOid, CI: contract.PrCINone}
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
	// The latest verdict at the current head from the viewer's account counts as a review, whether it is an issue comment or the
	// body of a review (GitHub refuses the author's own approval, so a COMMENTED review carries it). A statement's time is its
	// last edit, as its current body is what counts. A review's own commit is not compared: the verdict names its sha. A request
	// for changes from GitHub stands over an approval, and a later verdict replaces an earlier one.
	type statement struct {
		body   string
		author *gqlAuthor
		at     time.Time
	}
	var statements []statement
	for _, comment := range p.Comments.Nodes {
		at := comment.CreatedAt
		if comment.LastEditedAt != nil {
			at = *comment.LastEditedAt
		}
		statements = append(statements, statement{comment.Body, comment.Author, at})
	}
	for _, review := range p.Reviews.Nodes {
		if review.SubmittedAt == nil {
			continue
		}
		at := *review.SubmittedAt
		if review.LastEditedAt != nil {
			at = *review.LastEditedAt
		}
		statements = append(statements, statement{review.Body, review.Author, at})
	}
	sort.SliceStable(statements, func(i, j int) bool { return statements[i].at.Before(statements[j].at) })
	verdict := contract.PrReview("")
	for _, said := range statements {
		if match := verdictLine.FindStringSubmatch(said.body); match != nil && viewer != "" && login(said.author) == viewer && agentWrote(said.body) && strings.HasPrefix(p.HeadRefOid, match[2]) {
			verdict = contract.PrApproved
			if match[1] != "Approved" {
				verdict = contract.PrChangesRequested
			}
		}
	}
	if seen.Review != contract.PrChangesRequested && verdict != "" {
		seen.Review = verdict
	}
	// A rollup with no check in it says nothing about CI: GitHub reports success for it before any check has run. A rollup
	// of another commit than the head is an earlier push's, and the head's own checks have not reported yet.
	if len(p.Commits.Nodes) > 0 && p.Commits.Nodes[0].Commit.OID != p.HeadRefOid {
		seen.CI = contract.PrCIPending
	} else if len(p.Commits.Nodes) > 0 {
		seen.CI, seen.FailedChecks = summarizeCI(p.Commits.Nodes[0].Commit.StatusCheckRollup, p.Commits.Nodes[0].Commit.CheckSuites, ciPullHead, now)
	}
	for _, review := range p.Reviews.Nodes {
		if review.State == "PENDING" {
			continue
		}
		// GitHub's IDs are decimal strings. A malformed one reads as 0, which is never newer than a cursor.
		id, _ := strconv.ParseInt(review.ID, 10, 64)
		seen.Reviews = append(seen.Reviews, Review{ID: id, Author: login(review.Author), Body: review.Body, URL: review.URL})
	}
	return seen
}

// CommitCI is a commit's CI as its rollup shows it. Runs lists the first ciRunsRead checks, and Truncated says the rollup holds more.
type CommitCI struct {
	Result    contract.PrCI
	Runs      []contract.CIRun
	Truncated bool
}

// ciRunsRead is how many checks of a rollup one read lists, the most GitHub gives a page. A rollup with more is marked truncated and not paged, which would cost a query for each page.
const ciRunsRead = 100

// CommitsCI reads the check rollups of several default-branch commits in one query, one alias for each. A commit GitHub does not know has no CI.
func (c *Client) CommitsCI(ctx context.Context, repo contract.RepoName, shas []string) (map[string]CommitCI, error) {
	owner, name, ok := strings.Cut(string(repo), "/")
	if !ok || !ValidRepo(repo) {
		return nil, fmt.Errorf("invalid repository %q", repo)
	}
	variables := map[string]string{"owner": owner, "name": name}
	declared, fields := "", ""
	for i, sha := range shas {
		variables[fmt.Sprintf("h%d", i)] = sha
		declared += fmt.Sprintf(",$h%d:GitObjectID!", i)
		fields += fmt.Sprintf(" h%d:object(oid:$h%d){...ci}", i, i)
	}
	query := "query($owner:String!,$name:String!" + declared + "){viewer{login} repository(owner:$owner,name:$name){" + fields + "}}" +
		fmt.Sprintf(" fragment ci on Commit{"+suiteFields+" statusCheckRollup{state contexts(first:%d){totalCount pageInfo{hasNextPage} nodes{__typename ...on CheckRun{name status conclusion detailsUrl} ...on StatusContext{context state targetUrl}}}}}", ciRunsRead)
	repository, err := c.graphQL(ctx, query, variables)
	if err != nil {
		return nil, err
	}
	reads := make(map[string]CommitCI, len(shas))
	for i, sha := range shas {
		var commit struct {
			CheckSuites       gqlSuites  `json:"checkSuites"`
			StatusCheckRollup *gqlRollup `json:"statusCheckRollup"`
		}
		if raw := repository[fmt.Sprintf("h%d", i)]; raw != nil && string(raw) != "null" {
			if err := json.Unmarshal(raw, &commit); err != nil {
				return nil, fmt.Errorf("GitHub answer: %w", err)
			}
		}
		result, _ := summarizeCI(commit.StatusCheckRollup, commit.CheckSuites, ciMainHead, c.Now())
		read := CommitCI{Result: result}
		if rollup := commit.StatusCheckRollup; rollup != nil {
			read.Truncated = rollup.Contexts.PageInfo.HasNextPage || rollup.Contexts.TotalCount > len(rollup.Contexts.Nodes)
			for _, check := range rollup.Contexts.Nodes {
				run := contract.CIRun{Name: check.Name, Conclusion: check.Conclusion, URL: check.DetailsURL}
				if check.Type == "StatusContext" {
					run = contract.CIRun{Name: check.Context, Conclusion: check.State, URL: check.TargetURL}
				}
				read.Runs = append(read.Runs, run)
			}
		}
		reads[sha] = read
	}
	return reads, nil
}
