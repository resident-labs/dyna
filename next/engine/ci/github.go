package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// LeaseVariable is the repository variable that tells the workflow the pool is
// alive. It holds a Unix time. A pool that stops renewing it sends new jobs to
// GitHub-hosted runners once that time passes.
const LeaseVariable = "DYNA_CI_POOL_UNTIL"

// ErrPaused means the request budget of spec 7.7 stopped a call before it was sent.
var ErrPaused = errors.New("GitHub request budget: paused")

// Registration is a runner GitHub knows about.
type Registration struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Busy   bool   `json:"busy"`
}

// GitHub is the REST client of the pool. One client serves all repositories
// because they share one token and so one quota. Long polls and JIT configs go
// to the Actions service through Sets, and spend none of this quota.
type GitHub struct {
	Token     string
	Endpoint  *url.URL
	Transport http.RoundTripper
	Now       func() time.Time

	mu     sync.Mutex
	paused time.Time
	etags  map[contract.RepoName]etagged
}

type etagged struct {
	etag    string
	runners []Registration
}

type statusError struct {
	status  int
	message string
}

func (e statusError) Error() string { return fmt.Sprintf("GitHub HTTP %d: %s", e.status, e.message) }

func isStatus(err error, status int) bool {
	var failure statusError
	return errors.As(err, &failure) && failure.status == status
}

// observe keeps the pool inside spec 7.7. Below 20% of quota it waits at least
// ten minutes. A rate-limit refusal waits as long as GitHub asks.
func (g *GitHub) observe(header http.Header, status int) {
	now := g.Now()
	until := now
	limit, limitErr := strconv.Atoi(header.Get("X-RateLimit-Limit"))
	remaining, remainingErr := strconv.Atoi(header.Get("X-RateLimit-Remaining"))
	if limitErr == nil && remainingErr == nil && limit > 0 && remaining*5 < limit {
		until = now.Add(10 * time.Minute)
		if reset, err := strconv.ParseInt(header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			if at := time.Unix(reset, 0); at.After(until) {
				until = at
			}
		}
	}
	if seconds, err := strconv.Atoi(header.Get("Retry-After")); err == nil && (status == http.StatusForbidden || status == http.StatusTooManyRequests) {
		if at := now.Add(time.Duration(seconds) * time.Second); at.After(until) {
			until = at
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if until.After(g.paused) {
		g.paused = until
	}
}

func (g *GitHub) request(ctx context.Context, method, path string, input any, etag string) (*http.Response, []byte, error) {
	g.mu.Lock()
	paused := g.Now().Before(g.paused)
	g.mu.Unlock()
	if paused {
		return nil, nil, ErrPaused
	}
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return nil, nil, err
		}
		body = bytes.NewReader(data)
	}
	target := *g.Endpoint
	path, target.RawQuery, _ = strings.Cut(path, "?")
	target.Path = strings.TrimRight(target.Path, "/") + path
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+g.Token)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if etag != "" {
		request.Header.Set("If-None-Match", etag)
	}
	client := http.Client{Transport: g.Transport, Timeout: time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	g.observe(response.Header, response.StatusCode)
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, nil, err
	}
	if response.StatusCode >= 400 {
		var failure struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &failure)
		return response, nil, statusError{response.StatusCode, strings.ReplaceAll(failure.Message, g.Token, "***")}
	}
	return response, data, nil
}

// DeleteRunner removes a registration. One that is already gone is success.
func (g *GitHub) DeleteRunner(ctx context.Context, repo contract.RepoName, id int64) error {
	_, _, err := g.request(ctx, http.MethodDelete, "/repos/"+string(repo)+"/actions/runners/"+strconv.FormatInt(id, 10), nil, "")
	if isStatus(err, http.StatusNotFound) {
		return nil
	}
	return err
}

// ListRunners reads the first hundred registrations, which outnumber any pool.
func (g *GitHub) ListRunners(ctx context.Context, repo contract.RepoName) ([]Registration, error) {
	g.mu.Lock()
	cached := g.etags[repo]
	g.mu.Unlock()
	response, data, err := g.request(ctx, http.MethodGet, "/repos/"+string(repo)+"/actions/runners?per_page=100", nil, cached.etag)
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusNotModified {
		return cached.runners, nil
	}
	var result struct {
		Runners []Registration `json:"runners"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.etags == nil {
		g.etags = make(map[contract.RepoName]etagged)
	}
	g.etags[repo] = etagged{response.Header.Get("ETag"), result.Runners}
	return result.Runners, nil
}

// RenewLease writes the time until which new jobs may wait for this pool.
func (g *GitHub) RenewLease(ctx context.Context, repo contract.RepoName, until time.Time) error {
	input := map[string]string{"name": LeaseVariable, "value": strconv.FormatInt(until.Unix(), 10)}
	path := "/repos/" + string(repo) + "/actions/variables"
	_, _, err := g.request(ctx, http.MethodPatch, path+"/"+LeaseVariable, input, "")
	if isStatus(err, http.StatusNotFound) {
		_, _, err = g.request(ctx, http.MethodPost, path, input, "")
	}
	return err
}
