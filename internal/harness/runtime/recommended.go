package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"
)

// RecommendedHTTPClient permits offline metadata sources. Implementations must
// honor request contexts and must not add credentials or follow redirects.
type RecommendedHTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// RecommendedVersionResolver reads metadata only. Its zero value uses public
// HTTPS metadata and the current platform. It never runs a command or reads or
// writes personal files. HTTPClient and platform fields support offline tests.
type RecommendedVersionResolver struct {
	HTTPClient RecommendedHTTPClient
	GOOS       string
	GOARCH     string
	// Cache optionally reuses successes for six hours and failures for
	// one minute. Keep a
	// separate cache for each metadata source when injecting HTTPClient.
	Cache *RecommendedVersionCache
	// Refresh bypasses cached metadata for an explicit user refresh.
	Refresh bool
}

// RecommendedVersionCache stores metadata results in memory. Its zero value is
// ready to use. Expired successes are never used after a failed refresh.
type RecommendedVersionCache struct {
	mu      sync.Mutex
	entries map[string]recommendedCacheEntry
	now     func() time.Time
}

type recommendedCacheEntry struct {
	version string
	reason  string
	expires time.Time
}

// VersionCheckInterval is how long a recommended version stays fresh, and the
// interval the harness-versions hook starts with.
const VersionCheckInterval = 6 * time.Hour

var defaultRecommendedCache RecommendedVersionCache

// CheckVersions runs one version check pass and keeps the engine cache
// current while the UI is closed: every harness's recommended version is read
// afresh. The harness-versions built-in hook runs it on its own interval, and
// normal snapshot reads also populate the cache.
func CheckVersions(ctx context.Context) []RecommendedVersion {
	return RecommendedVersionResolver{Cache: &defaultRecommendedCache, Refresh: true}.RecommendedVersions(ctx)
}

func (c *RecommendedVersionCache) cached(key string) (string, string, bool) {
	if c == nil {
		return "", "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.now != nil {
		now = c.now()
	}
	entry := c.entries[key]
	if !now.Before(entry.expires) {
		delete(c.entries, key)
		return "", "", false
	}
	return entry.version, entry.reason, true
}

func (c *RecommendedVersionCache) remember(key, version, reason string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.now != nil {
		now = c.now()
	}
	if c.entries == nil {
		c.entries = make(map[string]recommendedCacheEntry)
	}
	ttl := VersionCheckInterval
	if version == "" {
		ttl = time.Minute
	}
	c.entries[key] = recommendedCacheEntry{version: version, reason: reason, expires: now.Add(ttl)}
}

// RecommendedVersions returns one result per registered adapter, in harness
// order. Sources run independently within a ten-second budget. Each request
// has two seconds and a 64 KiB response limit. Successes are cached for six
// hours, failures for one minute. Caller cancellation is never cached.
func RecommendedVersions(ctx context.Context) []RecommendedVersion {
	return (RecommendedVersionResolver{Cache: &defaultRecommendedCache}).RecommendedVersions(ctx)
}

func (r RecommendedVersionResolver) RecommendedVersions(ctx context.Context) []RecommendedVersion {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if r.HTTPClient == nil {
		r.HTTPClient = &http.Client{
			Timeout:       2 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	if r.GOOS == "" {
		r.GOOS = runtime.GOOS
	}
	if r.GOARCH == "" {
		r.GOARCH = runtime.GOARCH
	}
	adapters := RuntimeAdapters()
	out := make([]RecommendedVersion, len(adapters))
	var pending sync.WaitGroup
	for i, a := range adapters {
		pending.Add(1)
		go func() {
			defer pending.Done()
			v := RecommendedVersion{Harness: a.Harness, Status: "unavailable"}
			if a.distribution == distributionFork {
				v.Source = piForkSource()
				v.Version = piForkInstallVersion()
				v.Status = "resolved"
			} else if a.NPMPackage == "" {
				v.Reason = "The adapter has no verified version metadata source."
			} else {
				v.Source = "https://registry.npmjs.org/" + url.PathEscape(a.NPMPackage) + "/latest"
				v.Version, v.Reason = r.cached(ctx, a.Harness+":"+v.Source, func() (string, string) { return r.npmVersion(ctx, a, v.Source) })
				if v.Version != "" {
					v.Status = "resolved"
					v.Source = strings.TrimSuffix(v.Source, "/latest") + "/" + url.PathEscape(v.Version)
				}
			}
			out[i] = v
		}()
	}
	pending.Wait()
	return out
}

// cached runs lookup unless the cache holds a fresh result for key. Caller
// cancellation is never cached.
func (r RecommendedVersionResolver) cached(ctx context.Context, key string, lookup func() (string, string)) (string, string) {
	if !r.Refresh {
		if version, reason, ok := r.Cache.cached(key); ok {
			return version, reason
		}
	}
	version, reason := lookup()
	if ctx.Err() == nil {
		r.Cache.remember(key, version, reason)
	}
	return version, reason
}

// fetch reads one public metadata document. It returns the body, or a reason
// that never quotes the response.
func (r RecommendedVersionResolver) fetch(ctx context.Context, source, accept string) ([]byte, string) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, "Cannot create the version metadata request."
	}
	req.Header.Set("Accept", accept)
	resp, err := r.HTTPClient.Do(req)
	if err != nil || resp == nil || resp.Body == nil {
		return nil, "Version metadata is unavailable."
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "The version metadata source did not return HTTP 200."
	}
	const limit = 64 << 10
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(body) > limit {
		return nil, "Version metadata is unreadable or exceeds the response limit."
	}
	return body, ""
}

func (r RecommendedVersionResolver) npmVersion(ctx context.Context, a RuntimeAdapter, source string) (string, string) {
	return r.npmMetadata(ctx, a, source, "", "")
}

func (r RecommendedVersionResolver) npmMetadata(ctx context.Context, a RuntimeAdapter, source, expected, integrity string) (string, string) {
	body, reason := r.fetch(ctx, source, "application/json")
	if reason != "" {
		return "", reason
	}
	var metadata npmDistributionMetadata
	if json.Unmarshal(body, &metadata) != nil || metadata.Name != a.NPMPackage {
		return "", "Package metadata does not identify the adapter package."
	}
	v := metadata.Version
	if !exactVersion.MatchString(v) || !semver.IsValid("v"+v) {
		return "", "Package metadata does not contain an exact semantic version."
	}
	if ok, reason := supportedVersionRange(a.Harness, v); !ok {
		return "", reason
	}
	if !recommendedIntegrity(metadata.Dist.Integrity) {
		return "", "Package metadata has no valid distribution integrity."
	}
	if expected != "" {
		if v != expected || metadata.Dist.Integrity != integrity {
			return "", "Exact package metadata does not match the latest-version metadata."
		}
		return v, ""
	}
	return r.npmMetadata(ctx, a, strings.TrimSuffix(source, "/latest")+"/"+url.PathEscape(v), v, metadata.Dist.Integrity)
}

func recommendedIntegrity(value string) bool {
	for _, token := range strings.Fields(value) {
		algorithm, digest, ok := strings.Cut(token, "-")
		if !ok {
			continue
		}
		size := map[string]int{"sha256": 32, "sha384": 48, "sha512": 64}[algorithm]
		decoded, err := base64.StdEncoding.DecodeString(digest)
		if err == nil && size > 0 && len(decoded) == size {
			return true
		}
	}
	return false
}
