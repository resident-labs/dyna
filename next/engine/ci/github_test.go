package ci

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func client(t *testing.T, handler http.HandlerFunc) (*GitHub, *time.Time) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_000_000, 0)
	return &GitHub{Token: "token", Endpoint: endpoint, Transport: http.DefaultTransport, Now: func() time.Time { return now }}, &now
}

func TestFailuresNeverEchoTheToken(t *testing.T) {
	g, _ := client(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"bad token"}`))
	})
	err := g.RenewLease(context.Background(), repo, time.Unix(1, 0))
	if err == nil || strings.Contains(err.Error(), "token\"") || !strings.Contains(err.Error(), "bad ***") {
		t.Fatalf("error %v", err)
	}
}

func TestDeleteRunnerTreatsGoneAsDone(t *testing.T) {
	g, _ := client(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/repos/owner/repo/actions/runners/7" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
	})
	if err := g.DeleteRunner(context.Background(), repo, 7); err != nil {
		t.Fatal(err)
	}
}

func TestListRunnersSendsTheLastETag(t *testing.T) {
	calls := 0
	g, _ := client(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/repos/owner/repo/actions/runners" || r.URL.Query().Get("per_page") != "100" {
			t.Errorf("request %s", r.URL)
		}
		if calls == 2 {
			if r.Header.Get("If-None-Match") != `"v1"` {
				t.Errorf("If-None-Match %q", r.Header.Get("If-None-Match"))
			}
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(`{"runners":[{"id":1,"name":"dyna-ci-a","status":"offline","busy":false}]}`))
	})
	for range 2 {
		runners, err := g.ListRunners(context.Background(), repo)
		if err != nil || len(runners) != 1 || runners[0].Name != "dyna-ci-a" {
			t.Fatalf("%+v %v", runners, err)
		}
	}
}

func TestRenewLeaseCreatesAMissingVariable(t *testing.T) {
	var methods []string
	var value string
	g, _ := client(t, func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodPatch {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		value = body["value"]
		w.WriteHeader(http.StatusCreated)
	})
	until := time.Unix(2_000_000, 0)
	if err := g.RenewLease(context.Background(), repo, until); err != nil {
		t.Fatal(err)
	}
	want := []string{"PATCH /repos/owner/repo/actions/variables/" + LeaseVariable, "POST /repos/owner/repo/actions/variables"}
	if strings.Join(methods, ",") != strings.Join(want, ",") || value != "2000000" {
		t.Fatalf("%v value %q", methods, value)
	}
}

func TestLowQuotaPausesAtLeastTenMinutes(t *testing.T) {
	calls := 0
	g, now := client(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "999")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Unix(1_000_000, 0).Add(time.Minute).Unix(), 10))
	})
	if _, err := g.ListRunners(context.Background(), repo); err == nil || errors.Is(err, ErrPaused) {
		// The empty body fails to decode; only the header matters here.
		t.Log(err)
	}
	if _, err := g.ListRunners(context.Background(), repo); !errors.Is(err, ErrPaused) {
		t.Fatalf("error %v", err)
	}
	*now = now.Add(9 * time.Minute)
	if err := g.RenewLease(context.Background(), repo, time.Unix(1, 0)); !errors.Is(err, ErrPaused) {
		t.Fatalf("error %v", err)
	}
	if calls != 1 {
		t.Fatalf("%d requests reached GitHub", calls)
	}
	*now = now.Add(time.Minute)
	_, _ = g.ListRunners(context.Background(), repo)
	if calls != 2 {
		t.Fatalf("%d requests after the pause", calls)
	}
}

func TestRetryAfterPausesForAsLongAsGitHubAsks(t *testing.T) {
	g, now := client(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	if err := g.DeleteRunner(context.Background(), repo, 1); err == nil {
		t.Fatal("want the refusal")
	}
	if err := g.DeleteRunner(context.Background(), repo, 1); !errors.Is(err, ErrPaused) {
		t.Fatalf("error %v", err)
	}
	*now = now.Add(time.Minute)
	if err := g.DeleteRunner(context.Background(), repo, 1); errors.Is(err, ErrPaused) {
		t.Fatal("still paused")
	}
}
