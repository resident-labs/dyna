package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type recommendedClientFunc func(*http.Request) (*http.Response, error)

func (f recommendedClientFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func recommendedResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
}

func recommendedMetadata(pkg, version string) string {
	body, _ := json.Marshal(map[string]any{"name": pkg, "version": version, "dist": map[string]string{"integrity": "sha512-" + base64.StdEncoding.EncodeToString(make([]byte, 64))}})
	return string(body)
}

func TestRecommendedVersionsAdapterSources(t *testing.T) {
	packages := []string{}
	var mu sync.Mutex
	r := RecommendedVersionResolver{GOOS: "linux", GOARCH: "amd64", HTTPClient: recommendedClientFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" || req.URL.Scheme != "https" || req.URL.Host != "registry.npmjs.org" || req.Header.Get("Authorization") != "" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
		}
		deadline, ok := req.Context().Deadline()
		if !ok || time.Until(deadline) > 2*time.Second {
			t.Fatal("missing bounded request deadline")
		}
		path := req.URL.EscapedPath()
		pkg, _ := url.PathUnescape(strings.TrimPrefix(path[:strings.LastIndex(path, "/")], "/"))
		if strings.HasSuffix(path, "/latest") {
			mu.Lock()
			packages = append(packages, pkg)
			mu.Unlock()
		}
		version := "1.3.0"
		return recommendedResponse(200, recommendedMetadata(pkg, version)), nil
	})}
	got := r.RecommendedVersions(context.Background())
	if len(got) != 2 {
		t.Fatalf("got %d adapters", len(got))
	}
	sort.Strings(packages)
	if !reflect.DeepEqual(packages, []string{"@anthropic-ai/claude-code", piNPMPackage}) {
		t.Fatalf("incorrect adapter packages: %v", packages)
	}
	for i, v := range got {
		if i > 0 && got[i-1].Harness >= v.Harness {
			t.Fatal("results are not sorted")
		}
		if v.Status != "resolved" || v.Version == "" || v.Reason != "" {
			t.Fatalf("bad npm result: %+v", v)
		}
	}
}

func TestRecommendedVersionsMetadataFailure(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"http", `secret response`, 503},
		{"redirect", `secret response`, 302},
		{"malformed", `{`, 200},
		{"trailing", `{"name":"` + piNPMPackage + `","version":"1.3.0"} {}`, 200},
		{"wrong-package", `{"name":"not-the-pi-package","version":"2.1.0"}`, 200},
		{"tag", `{"name":"` + piNPMPackage + `","version":"latest"}`, 200},
		{"range", `{"name":"` + piNPMPackage + `","version":"^1.3.0"}`, 200},
		{"invalid-semver", `{"name":"` + piNPMPackage + `","version":"1.3.0-01"}`, 200},
		{"missing-integrity", `{"name":"` + piNPMPackage + `","version":"1.3.0"}`, 200},
		{"bad-integrity", `{"name":"` + piNPMPackage + `","version":"1.3.0","dist":{"integrity":"sha512-secret"}}`, 200},
		{"oversized", strings.Repeat(" ", (64<<10)+1), 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := RecommendedVersionResolver{HTTPClient: recommendedClientFunc(func(*http.Request) (*http.Response, error) { return recommendedResponse(tc.status, tc.body), nil })}
			a, _ := Adapter("pi")
			v, reason := r.npmVersion(context.Background(), a, "https://registry.npmjs.org/example/latest")
			if v != "" || reason == "" || strings.Contains(reason, "secret") {
				t.Fatalf("unverified version or exposed response: %q %q", v, reason)
			}
		})
	}
}

func TestRecommendedVersionsExactMetadataMismatch(t *testing.T) {
	for _, change := range []string{"version", "integrity"} {
		t.Run(change, func(t *testing.T) {
			calls := 0
			r := RecommendedVersionResolver{HTTPClient: recommendedClientFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				body := recommendedMetadata(piNPMPackage, "1.3.0")
				if calls == 2 {
					if !strings.HasSuffix(req.URL.Path, "/1.3.0") {
						t.Fatalf("wrong exact source: %s", req.URL)
					}
					if change == "version" {
						body = recommendedMetadata(piNPMPackage, "1.1.1")
					} else {
						body = strings.ReplaceAll(body, base64.StdEncoding.EncodeToString(make([]byte, 64)), base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 64))))
					}
				}
				return recommendedResponse(200, body), nil
			})}
			a, _ := Adapter("pi")
			v, reason := r.npmVersion(context.Background(), a, "https://registry.npmjs.org/example/latest")
			if v != "" || reason == "" || calls != 2 {
				t.Fatalf("mismatched metadata accepted: %q, %q, %d requests", v, reason, calls)
			}
		})
	}
}

func TestRecommendedVersionsOfflineAndUnsupported(t *testing.T) {
	r := RecommendedVersionResolver{GOOS: "windows", GOARCH: "amd64", HTTPClient: recommendedClientFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("secret transport details")
	})}
	for _, v := range r.RecommendedVersions(context.Background()) {
		if v.Version != "" || v.Reason == "" || strings.Contains(v.Reason, "secret") {
			t.Fatalf("bad offline result: %+v", v)
		}
		if v.Status != "unavailable" {
			t.Fatalf("bad platform status: %+v", v)
		}
	}
}

func TestRecommendedVersionsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := RecommendedVersionResolver{GOOS: "linux", GOARCH: "amd64", HTTPClient: recommendedClientFunc(func(req *http.Request) (*http.Response, error) {
		if req.Context().Err() == nil {
			t.Fatal("lost cancellation")
		}
		return nil, req.Context().Err()
	})}
	got := r.RecommendedVersions(ctx)
	if len(got) != 2 {
		t.Fatal("cancellation removed per-harness results")
	}
	for _, v := range got {
		if v.Status == "resolved" {
			t.Fatalf("resolved cancelled request: %+v", v)
		}
	}
}

func TestRecommendedVersionsCache(t *testing.T) {
	now := time.Unix(100, 0)
	cache := &RecommendedVersionCache{now: func() time.Time { return now }}
	calls, offline := 0, false
	var mu sync.Mutex
	r := RecommendedVersionResolver{Cache: cache, GOOS: "linux", GOARCH: "amd64", HTTPClient: recommendedClientFunc(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if offline {
			return nil, errors.New("offline")
		}
		path := req.URL.Path
		pkg := strings.TrimPrefix(path[:strings.LastIndex(path, "/")], "/")
		version := "2.1.0"
		return recommendedResponse(200, recommendedMetadata(pkg, version)), nil
	})}
	first := r.RecommendedVersions(context.Background())
	if calls != 4 {
		t.Fatalf("first lookup made %d requests", calls)
	}
	offline = true
	now = now.Add(6*time.Hour - time.Second)
	if got := r.RecommendedVersions(context.Background()); !reflect.DeepEqual(got, first) || calls != 4 {
		t.Fatalf("fresh cache missed: %d requests, %+v", calls, got)
	}
	now = now.Add(time.Second)
	for _, v := range r.RecommendedVersions(context.Background()) {
		if v.Status == "resolved" || (v.Harness == "pi" && v.Reason == "") {
			t.Fatalf("expired result reused: %+v", v)
		}
	}
	if calls != 6 {
		t.Fatalf("expired cache made %d requests", calls)
	}
	offline = false
	for _, v := range r.RecommendedVersions(context.Background()) {
		if v.Status == "resolved" {
			t.Fatalf("failure cache reused stale success: %+v", v)
		}
	}
	if calls != 6 {
		t.Fatalf("failure cache missed: %d requests", calls)
	}
	now = now.Add(time.Minute)
	if got := r.RecommendedVersions(context.Background()); !reflect.DeepEqual(got, first) || calls != 10 {
		t.Fatalf("expired failure cache did not recover: %d requests, %+v", calls, got)
	}
}

// One slow vendor must not consume the budget before other vendors start.
func TestRecommendedSourcesRunIndependently(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan string, 5)
	r := RecommendedVersionResolver{GOOS: "linux", GOARCH: "amd64", HTTPClient: recommendedClientFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/latest") {
			started <- req.URL.String()
		}
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}
	done := make(chan []RecommendedVersion, 1)
	go func() { done <- r.RecommendedVersions(ctx) }()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	// Every source makes a request.
	for range 2 {
		select {
		case <-started:
		case <-deadline.C:
			cancel()
			<-done
			t.Fatal("one vendor blocked other sources")
		}
	}
	cancel()
	if len(<-done) != 2 {
		t.Fatal("lost per-harness results")
	}
}

func TestRecommendedRefreshBypassesSixHourCache(t *testing.T) {
	now := time.Unix(100, 0)
	cache := &RecommendedVersionCache{now: func() time.Time { return now }}
	r := RecommendedVersionResolver{Cache: cache}
	calls := 0
	lookup := func() (string, string) { calls++; return "2.1.303", "" }
	ctx := context.Background()
	r.cached(ctx, "claude-code", lookup)
	now = now.Add(5 * time.Hour)
	r.cached(ctx, "claude-code", lookup)
	if calls != 1 {
		t.Fatal("cache expired before six hours")
	}
	r.Refresh = true
	r.cached(ctx, "claude-code", lookup)
	if calls != 2 {
		t.Fatal("Refresh reused cached metadata")
	}
	r.Refresh = false
	now = now.Add(time.Hour)
	r.cached(ctx, "claude-code", lookup)
	if calls != 2 {
		t.Fatal("Refresh did not replace cached metadata")
	}
}
