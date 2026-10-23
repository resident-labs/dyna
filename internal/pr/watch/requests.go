package watch

import (
	"bufio"
	"bytes"
	"context"
	"dyna-agent/internal/ghclient"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/pr/state"
)

// cachedResponse belongs to one exact request path, including its page query.
// Keep the body and next link because a 304 response need not include either.
type cachedResponse = state.CachedResponse

type RateLimit = state.RateLimit
type RateLimitScope = state.RateLimitScope

func reserve(r RateLimit, now time.Time) bool {
	return r.Remaining != nil && *r.Remaining < 500 && now.Before(r.Reset)
}

type rateLimitError struct{ until time.Time }

func (e *rateLimitError) Error() string {
	return "GitHub polling paused by rate limit until " + e.until.Format(time.RFC3339)
}

type requestCache struct {
	forceBudgets  bool // Existing full polls also refresh completed timing evidence.
	forceSnapshot bool // Reconcile a committed budget through the normal full poll.
	cacheOnly     bool // Read saved CI evidence without transport or entry mutation.
	entries       map[string]cachedResponse
	used          map[string]cachedResponse
	notModified   map[string]bool
	rates         map[string]RateLimit
	legacyPause   time.Time
	account       string
	rateMu        *sync.Mutex
	now           func() time.Time
}

func (g GitHub) request(ctx context.Context, r Ref, path string) ([]byte, string, error) {
	return g.requestMethod(ctx, r, path, "GET")
}

func (g GitHub) requestMethod(ctx context.Context, r Ref, path, method string, extraArgs ...string) ([]byte, string, error) {
	return g.requestBody(ctx, r, path, method, false, extraArgs...)
}

// requestBody shares HTTP status and rate-limit handling for JSON and optional job logs.
func (g GitHub) requestBody(ctx context.Context, r Ref, path, method string, rawBody bool, extraArgs ...string) ([]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	now := time.Now()
	if g.requests != nil {
		now = g.requests.clock()
	}
	var cached cachedResponse
	key := strings.ToLower(r.Host) + "/" + path
	if g.requests != nil {
		if method == "GET" {
			cached = g.requests.entries[key]
		}
	}
	if g.requests != nil && g.requests.cacheOnly {
		if method != "GET" || len(cached.Body) == 0 {
			return nil, "", fmt.Errorf("saved GitHub response is unavailable")
		}
		g.requests.use(key, cached)
		return cached.Body, cached.Next, nil
	}
	// A file at a full commit SHA never changes. A validated cached copy
	// needs no request, which saves one gh process per run on every budget check.
	if cached.ETag != "" && len(cached.Body) > 0 && fixedContent(path) {
		g.requests.use(key, cached)
		return cached.Body, cached.Next, nil
	}
	args := []string{"api", "--hostname", r.Host, path, "--include"}
	if method != "GET" {
		args = append(args, "--method", method)
	}
	if cached.ETag != "" {
		args = append(args, "-H", "If-None-Match: "+cached.ETag)
	}
	args = append(args, extraArgs...)
	limit := 4 * 1024 * 1024
	if rawBody {
		limit = maxTestArtifact
	}
	if r.Repo != "" {
		ctx = ghclient.WithRepository(ctx, r.Host, r.Repo)
	}
	scope := RateLimitScope{Host: r.Host, Resource: "core"}.Normalize()
	if path == "graphql" {
		scope.Resource = "graphql"
	}
	before := func(account string) error {
		scope.Account = strings.ToLower(account)
		if g.requests == nil {
			return nil
		}
		g.requests.account = scope.Account
		until := g.requests.pauseUntil(scope)
		if now.Before(until) {
			return &rateLimitError{until}
		}
		return nil
	}
	observedAt := time.Now().UTC()
	raw, commandErr := g.commandWithAccount(ctx, "", false, limit, before, args...)
	// gh prints HTTP headers even on HTTP errors. Do not read or report stderr.
	reader := bufio.NewReader(bytes.NewReader(raw))
	line, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "HTTP/") {
		if commandErr != nil {
			return nil, "", commandErr
		}
		return nil, "", fmt.Errorf("gh returned no HTTP status")
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return nil, "", fmt.Errorf("invalid GitHub HTTP status")
	}
	status, err := strconv.Atoi(fields[1])
	if err != nil {
		return nil, "", fmt.Errorf("invalid GitHub HTTP status")
	}
	// A 304 reply validates a saved copy and costs no REST quota.
	engineGHQuota.record(path, status == http.StatusNotModified)
	headers, err := textproto.NewReader(reader).ReadMIMEHeader()
	if err != nil {
		return nil, "", fmt.Errorf("invalid GitHub HTTP headers")
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, "", err
	}
	rate := &RateLimit{}
	if g.requests != nil {
		defer g.requests.lockRate()()
		value := g.requests.rates[scope.Key()]
		rate = &value
		defer func() {
			if g.requests.rates == nil {
				g.requests.rates = map[string]RateLimit{}
			}
			g.requests.rates[scope.Key()] = *rate
		}()
	}
	if observedAt.After(rate.ObservedAt) {
		rate.ObservedAt = observedAt
	}
	// A response for a different resource cannot change the requested resource's quota.
	matching := headers.Get("X-Ratelimit-Resource") == "" || strings.EqualFold(headers.Get("X-Ratelimit-Resource"), scope.Resource)
	reset := rate.Reset
	if n, err := strconv.ParseInt(headers.Get("X-Ratelimit-Reset"), 10, 64); err == nil && matching {
		reset = time.Unix(n, 0).UTC()
	}
	if reset.After(rate.Reset) {
		rate.Reset = reset
		rate.Remaining = nil
	}
	if n, err := strconv.Atoi(headers.Get("X-Ratelimit-Remaining")); err == nil && matching && reset.Equal(rate.Reset) {
		// Replies in one quota window can only decrease its remaining count.
		if rate.Remaining == nil || n < *rate.Remaining {
			rate.Remaining = &n
		}
	}
	primary := matching && headers.Get("X-Ratelimit-Remaining") == "0" && !reset.Before(rate.Reset)
	secondary := strings.Contains(strings.ToLower(string(body)), "secondary rate limit") || strings.Contains(strings.ToLower(string(body)), "abuse detection")
	if (status == 403 || status == 429) && (primary || secondary || headers.Get("Retry-After") != "") {
		delay := time.Minute
		if secondary {
			rate.SecondaryFailures++
			delay *= time.Duration(1 << min(rate.SecondaryFailures-1, 6))
		}
		until := now.Add(delay)
		if primary && rate.Reset.After(now) {
			until = rate.Reset.Add(time.Second)
		}
		if seconds, err := strconv.Atoi(headers.Get("Retry-After")); err == nil && seconds >= 0 {
			if retry := now.Add(time.Duration(seconds) * time.Second); retry.After(until) {
				until = retry
			}
		} else if retry, err := http.ParseTime(headers.Get("Retry-After")); err == nil && retry.After(until) {
			until = retry
		}
		if until.After(rate.PauseUntil) {
			rate.PauseUntil, rate.Reason, rate.PauseScope = until, "GitHub rate limit", scope
		}
		return nil, "", &rateLimitError{until}
	}
	if status == http.StatusNotModified {
		if cached.ETag == "" || len(cached.Body) == 0 {
			return nil, "", fmt.Errorf("GitHub returned 304 without a cached response")
		}
		if !now.Before(rate.PauseUntil) {
			rate.PauseUntil, rate.Reason, rate.PauseScope = time.Time{}, "", RateLimitScope{}
		}
		if etag := headers.Get("ETag"); etag != "" {
			cached.ETag = etag
		}
		if g.requests != nil {
			g.requests.use(key, cached)
			if g.requests.notModified == nil {
				g.requests.notModified = map[string]bool{}
			}
			g.requests.notModified[key] = true
		}
		return cached.Body, cached.Next, nil
	}
	if status >= 400 && commandErr != nil {
		return nil, "", fmt.Errorf("GitHub HTTP status %d: %w", status, commandErr)
	}
	if commandErr != nil {
		// GraphQL can return usable partial data with a command error.
		if status >= 200 && status < 300 && json.Valid(body) {
			return body, "", commandErr
		}
		return nil, "", commandErr
	}
	if status < 200 || status >= 300 {
		return nil, "", fmt.Errorf("GitHub HTTP status %d", status)
	}
	if !now.Before(rate.PauseUntil) {
		rate.PauseUntil, rate.Reason, rate.PauseScope = time.Time{}, "", RateLimitScope{}
	}
	// Explicit service calls do not carry the polling cache. Their fresh
	// quota observation can prove that an outage's saved pause is stale.
	if g.requests == nil && g.recoverPolling != nil {
		if err := g.recoverPolling(*rate, scope); err != nil {
			return nil, "", err
		}
	}
	// Retain the failure count across successful requests so a repeated secondary
	// limit uses an increasing delay, rather than a one-minute retry loop.
	if method != "GET" && len(bytes.TrimSpace(body)) == 0 {
		return nil, "", nil
	}
	if rawBody {
		return body, "", nil
	}
	if !json.Valid(body) {
		return nil, "", fmt.Errorf("GitHub returned invalid JSON")
	}
	next := ""
	for _, link := range strings.Split(headers.Get("Link"), ",") {
		target, rel, ok := strings.Cut(strings.TrimSpace(link), ";")
		if !ok || !strings.Contains(rel, `rel="next"`) {
			continue
		}
		u, err := url.Parse(strings.Trim(target, "<>"))
		if err != nil || u.User != nil || u.Scheme != "https" {
			return nil, "", fmt.Errorf("invalid GitHub pagination link")
		}
		host := "api.github.com"
		if !strings.EqualFold(r.Host, "github.com") {
			host = r.Host
		}
		if !strings.EqualFold(u.Host, host) {
			return nil, "", fmt.Errorf("unexpected GitHub pagination host")
		}
		next = strings.TrimPrefix(u.EscapedPath(), "/")
		if !strings.EqualFold(r.Host, "github.com") {
			next = strings.TrimPrefix(next, "api/v3/")
		}
		if u.RawQuery != "" {
			next += "?" + u.RawQuery
		}
	}
	if g.requests != nil && method == "GET" {
		entry := cachedResponse{ETag: headers.Get("ETag"), Body: append(json.RawMessage(nil), body...), Next: next}
		g.requests.use(key, entry)
	}
	return body, next, nil
}

// clock is the cache's time source; a cache built without one uses the wall clock.
func (c *requestCache) clock() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

// use records the response this poll relied on, for the next poll's cache.
func (c *requestCache) use(key string, response cachedResponse) {
	if c.used == nil {
		c.used = map[string]cachedResponse{}
	}
	c.used[key] = response
}

// fixedContent reports whether path reads repository contents at a full commit SHA.
func fixedContent(path string) bool {
	endpoint, query, _ := strings.Cut(path, "?")
	values, _ := url.ParseQuery(query)
	ref := values.Get("ref")
	return strings.Contains(endpoint, "/contents/") && (len(ref) == 40 || len(ref) == 64) && headSHAPattern.MatchString(ref)
}

// Each worker owns its response cache. Only the rate limit is shared.
func (c *requestCache) lockRate() func() {
	if c.rateMu == nil {
		return func() {}
	}
	c.rateMu.Lock()
	return c.rateMu.Unlock
}

// applyRateLimits updates scoped entries and spreads only the recovered records.
func applyRateLimits(st *State, rates map[string]RateLimit, now time.Time) {
	recovered := map[string]bool{}
	for key, rate := range rates {
		previous := st.RateLimits[key]
		if rate.ObservedAt.Before(previous.ObservedAt) {
			continue
		}
		// A stale snapshot must not increase quota or erase a later pause.
		if previous.Reset.After(rate.Reset) {
			continue
		}
		if previous.Reset.Equal(rate.Reset) && previous.Remaining != nil && rate.Remaining != nil && *previous.Remaining < *rate.Remaining {
			rate.Remaining = previous.Remaining
		}
		if !previous.PauseUntil.IsZero() && rate.PauseUntil.IsZero() {
			recovered[key] = true
		}
		st.RateLimits[key] = rate
	}
	st.ExpireLegacyPause(now)
	st.RefreshQuotaDisplay()
	var keys []string
	for key, r := range st.Records {
		if !r.Watching || changeKind(*r) != "github" || now.Before(quotaFor(*st, r, now).PauseUntil) {
			continue
		}
		for _, resource := range []string{"core", "graphql"} {
			if recovered[(RateLimitScope{Host: r.Host, Account: r.QuotaAccount, Resource: resource}).Key()] {
				keys = append(keys, key)
				break
			}
		}
	}
	sort.Strings(keys)
	for i, key := range keys {
		r := st.Records[key]
		r.Polling.Next = time.Time{}
		if slot := i / 10; slot > 0 {
			r.Polling.Next = nextPollTime(now, 1).Add(time.Duration(slot-1) * time.Minute)
		}
		r.Polling.FullChecked = time.Time{}
		r.Polling.IntervalMinutes = 1
	}
	for _, r := range st.Records {
		if r.Watching && reserve(quotaFor(*st, r, now), now) {
			r.Polling.IntervalMinutes = 5
			if next := r.Checked.Add(5 * time.Minute); next.After(r.Polling.Next) {
				r.Polling.Next = next
			}
		}
	}
}

// ratesNeedAllRecords checks only isolated metadata. applyRateLimits remains
// the single rate merge implementation. Recovery and reserve scheduling must
// see every record; ordinary quota display changes are derived by state.write.
func ratesNeedAllRecords(st *State, rates map[string]RateLimit, now time.Time) bool {
	metadata := State{RateLimits: cloneRates(st.RateLimits), LegacyPause: st.LegacyPause}
	applyRateLimits(&metadata, rates, now)
	for key, previous := range st.RateLimits {
		if !previous.PauseUntil.IsZero() && metadata.RateLimits[key].PauseUntil.IsZero() {
			return true
		}
	}
	for _, rate := range metadata.RateLimits {
		if reserve(rate, now) {
			return true
		}
	}
	return false
}

func quotaFor(st State, r *Record, now time.Time) RateLimit {
	return st.Quota(r.Host, r.QuotaAccount, now)
}

// recoverPolling accepts a fresh successful request as proof only for the
// original quota scope. Unknown legacy pauses stay intact until their deadline.
func (s *Service) recoverPolling(rate RateLimit, scope RateLimitScope) error {
	scope = scope.Normalize()
	if !scope.Known() || rate.Remaining == nil || *rate.Remaining <= 0 {
		return nil
	}
	st, err := s.Store.Read()
	if err != nil {
		return err
	}
	if st.RateLimits[scope.Key()].PauseUntil.IsZero() {
		return nil
	}
	return s.Store.update(func(st *State) error {
		previous := st.RateLimits[scope.Key()]
		if previous.PauseUntil.IsZero() || rate.ObservedAt.Before(previous.ObservedAt) {
			return nil
		}
		if rate.Reset.IsZero() {
			rate.Reset = previous.Reset
		}
		if rate.Reset.Before(previous.Reset) {
			return nil
		}
		rate.SecondaryFailures = previous.SecondaryFailures
		rate.PauseUntil, rate.Reason, rate.PauseScope = time.Time{}, "", RateLimitScope{}
		// Fresh same-scope proof can restore quota within the saved outage window.
		previous.Remaining = nil
		st.RateLimits[scope.Key()] = previous
		applyRateLimits(st, map[string]RateLimit{scope.Key(): rate}, s.now())
		return nil
	})
}

func cloneRates(rates map[string]RateLimit) map[string]RateLimit {
	result := make(map[string]RateLimit, len(rates))
	for key, rate := range rates {
		result[key] = rate
	}
	return result
}

func (c *requestCache) pauseUntil(scope RateLimitScope) time.Time {
	defer c.lockRate()()
	until := c.rates[scope.Key()].PauseUntil
	if c.legacyPause.After(until) {
		until = c.legacyPause
	}
	return until
}
