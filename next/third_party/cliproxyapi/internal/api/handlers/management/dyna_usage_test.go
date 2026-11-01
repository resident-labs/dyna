package management

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/tidwall/gjson"
)

var dynaUsageNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// dynaFakeUsage answers every reader in these tests. Any request it does not
// expect fails, so no test can reach a provider.
type dynaFakeUsage struct {
	mu       sync.Mutex
	requests []*http.Request
	respond  func(*http.Request) (*http.Response, error)
}

func (f *dynaFakeUsage) RoundTrip(request *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	f.mu.Unlock()
	return f.respond(request)
}

func dynaResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func dynaFixedUsage(t *testing.T, status int, body string) *dynaFakeUsage {
	t.Helper()
	fake := &dynaFakeUsage{respond: func(*http.Request) (*http.Response, error) { return dynaResponse(status, body), nil }}
	dynaUseTransport(t, fake)
	return fake
}

func dynaUseTransport(t *testing.T, fake *dynaFakeUsage) {
	t.Helper()
	previous := dynaUsageTransport
	dynaUsageTransport = func(*Handler, *coreauth.Auth) http.RoundTripper { return fake }
	t.Cleanup(func() { dynaUsageTransport = previous })
}

func dynaUsageHandler(t *testing.T, auths ...*coreauth.Auth) *Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	for _, auth := range auths {
		if _, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), auth); errRegister != nil {
			t.Fatal(errRegister)
		}
	}
	return NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
}

func dynaCodexAuth(id string) *coreauth.Auth {
	return &coreauth.Auth{ID: id, Provider: "codex", Metadata: map[string]any{
		"auth_kind": "oauth", "access_token": "invented-access", "refresh_token": "invented-refresh", "account_id": "invented-account",
	}}
}

func dynaOneEntry(t *testing.T, h *Handler) authFileQuotaEntry {
	t.Helper()
	files := runAuthFileQuota(t, h, "").Files
	if len(files) != 1 {
		t.Fatalf("files = %#v, want one", files)
	}
	return files[0]
}

func dynaParse(t *testing.T, parse func(gjson.Result, time.Time) dynaUsage, body string) dynaUsage {
	t.Helper()
	if !gjson.Valid(body) {
		t.Fatalf("invalid fixture: %s", body)
	}
	return parse(gjson.Parse(body), dynaUsageNow)
}

func dynaWindowNamed(t *testing.T, usage dynaUsage, name string) authFileQuotaWindow {
	t.Helper()
	for _, window := range usage.windows {
		if window.Name == name {
			return window
		}
	}
	t.Fatalf("no window %q in %#v", name, usage.windows)
	return authFileQuotaWindow{}
}

func TestDynaUsageCodexClassifiesWindowsByLength(t *testing.T) {
	usage := dynaParse(t, parseCodexUsage, `{"rate_limit":{
		"primary_window":{"used_percent":20,"limit_window_seconds":604800,"reset_at":1791800000},
		"secondary_window":{"used_percent":42,"limit_window_seconds":18000,"reset_after_seconds":3600}}}`)
	weekly, session := dynaWindowNamed(t, usage, "seven_day"), dynaWindowNamed(t, usage, "five_hour")
	if weekly.Kind != "weekly" || weekly.Utilization != 20 || session.Kind != "five_hour" || session.Utilization != 42 {
		t.Fatalf("windows = %#v", usage.windows)
	}
	if session.ResetsAt != dynaUsageNow.Add(time.Hour).Format(time.RFC3339) {
		t.Fatalf("relative reset = %q", session.ResetsAt)
	}
}

func TestDynaUsageCodexAdditionalLimitsAsArrayOrObject(t *testing.T) {
	limit := `"rate_limit":{"primary_window":{"used_percent":7,"limit_window_seconds":18000}}`
	for _, tc := range []struct{ name, body string }{
		{"array", `{"additional_rate_limits":[{"limit_name":"Spark",` + limit + `}]}`},
		{"object", `{"additional_rate_limits":{"Spark":{` + limit + `}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if window := dynaWindowNamed(t, dynaParse(t, parseCodexUsage, tc.body), "Spark five_hour"); window.Scope != "Spark" || window.Utilization != 7 {
				t.Fatalf("window = %#v", window)
			}
		})
	}
}

func TestDynaUsageCodexUnknownShapeIsNotZero(t *testing.T) {
	for _, body := range []string{`{}`, `{"rate_limit":null}`, `{"rate_limit":{"primary_window":{"limit_window_seconds":18000}}}`, `{"rate_limit":{"primary_window":{"used_percent":"high"}}}`} {
		if usage := dynaParse(t, parseCodexUsage, body); len(usage.windows) != 0 {
			t.Fatalf("%s produced %#v", body, usage.windows)
		}
	}
}

func TestDynaUsageCodexRequestUsesTheStoredCredential(t *testing.T) {
	fake := dynaFixedUsage(t, http.StatusOK, `{"rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":18000}}}`)
	entry := dynaOneEntry(t, dynaUsageHandler(t, dynaCodexAuth("invented")))
	if entry.State != dynaUsageOK || entry.Provider != "codex" || len(entry.Windows) != 1 {
		t.Fatalf("entry = %#v", entry)
	}
	request := fake.requests[0]
	if request.URL.String() != "https://chatgpt.com/backend-api/wham/usage" || request.Header.Get("Authorization") != "Bearer invented-access" || request.Header.Get("Chatgpt-Account-Id") != "invented-account" {
		t.Fatalf("request = %s %#v", request.URL, request.Header)
	}
}

func TestDynaUsageFailuresStayPerAccount(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		state  string
	}{
		{"unauthorized", http.StatusUnauthorized, ``, dynaUsageUnreachable},
		{"forbidden", http.StatusForbidden, ``, dynaUsageUnreachable},
		{"missing", http.StatusNotFound, ``, dynaUsageUnreachable},
		{"server", http.StatusBadGateway, `<html>`, dynaUsageUnreachable},
		{"rate limited", http.StatusTooManyRequests, ``, dynaUsageRateLimited},
		{"not json", http.StatusOK, `<html>`, dynaUsageUnknown},
		{"no windows", http.StatusOK, `{}`, dynaUsageUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dynaFixedUsage(t, tc.status, tc.body)
			entry := dynaOneEntry(t, dynaUsageHandler(t, dynaCodexAuth("invented")))
			if entry.State != tc.state || len(entry.Windows) != 0 {
				t.Fatalf("entry = %#v", entry)
			}
			if strings.Contains(entry.Error, "<html>") {
				t.Fatal("upstream body was forwarded")
			}
		})
	}
}

func TestDynaUsageTransportFailureIsUnreachable(t *testing.T) {
	dynaUseTransport(t, &dynaFakeUsage{respond: func(*http.Request) (*http.Response, error) {
		return nil, errors.New("invented-access refused")
	}})
	entry := dynaOneEntry(t, dynaUsageHandler(t, dynaCodexAuth("invented")))
	if entry.State != dynaUsageUnreachable || strings.Contains(entry.Error, "invented-access") {
		t.Fatalf("entry = %#v", entry)
	}
}

func TestDynaUsageDoesNotFollowRedirects(t *testing.T) {
	fake := &dynaFakeUsage{respond: func(*http.Request) (*http.Response, error) {
		response := dynaResponse(http.StatusFound, ``)
		response.Header.Set("Location", "https://elsewhere.example/steal")
		return response, nil
	}}
	dynaUseTransport(t, fake)
	if entry := dynaOneEntry(t, dynaUsageHandler(t, dynaCodexAuth("invented"))); entry.State != dynaUsageUnreachable {
		t.Fatalf("entry = %#v", entry)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(fake.requests))
	}
}

func TestDynaUsageSkipsDisabledCredentials(t *testing.T) {
	fake := dynaFixedUsage(t, http.StatusOK, `{}`)
	auth := dynaCodexAuth("invented")
	auth.Disabled = true
	if entry := dynaOneEntry(t, dynaUsageHandler(t, auth)); entry.State != dynaUsageUnknown {
		t.Fatalf("entry = %#v", entry)
	}
	if len(fake.requests) != 0 {
		t.Fatal("a staged credential reached the provider")
	}
}

func TestDynaUsageIsLimitedToFourConcurrentReads(t *testing.T) {
	var active, peak atomic.Int32
	release := make(chan struct{})
	fake := &dynaFakeUsage{respond: func(*http.Request) (*http.Response, error) {
		now := active.Add(1)
		defer active.Add(-1)
		for {
			seen := peak.Load()
			if now <= seen || peak.CompareAndSwap(seen, now) {
				break
			}
		}
		<-release
		return dynaResponse(http.StatusOK, `{}`), nil
	}}
	dynaUseTransport(t, fake)
	auths := make([]*coreauth.Auth, 9)
	for i := range auths {
		auths[i] = dynaCodexAuth("invented-" + string(rune('a'+i)))
	}
	h := dynaUsageHandler(t, auths...)
	done := make(chan []authFileQuotaEntry, 1)
	go func() { done <- runAuthFileQuota(t, h, "").Files }()
	for peak.Load() < 4 {
		time.Sleep(time.Millisecond)
	}
	close(release)
	if files := <-done; len(files) != len(auths) {
		t.Fatalf("files = %d, want %d", len(files), len(auths))
	}
	if peak.Load() > authFileQuotaConcurrency {
		t.Fatalf("peak = %d, want at most %d", peak.Load(), authFileQuotaConcurrency)
	}
}

// dynaRefreshCounter counts refreshes. Nothing else on it runs in these tests.
type dynaRefreshCounter struct {
	coreauth.ProviderExecutor
	provider string
	calls    atomic.Int32
}

func (e *dynaRefreshCounter) Identifier() string { return e.provider }

func (e *dynaRefreshCounter) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	e.calls.Add(1)
	return auth, nil
}

func TestDynaUsageNeverRefreshes(t *testing.T) {
	counter := &dynaRefreshCounter{provider: "codex"}
	auth := dynaCodexAuth("invented")
	auth.Metadata["expired"] = dynaUsageNow.Add(-time.Hour).Format(time.RFC3339)
	h := dynaUsageHandler(t, auth)
	h.authManager.RegisterExecutor(counter)
	fake := dynaFixedUsage(t, http.StatusUnauthorized, ``)
	if entry := dynaOneEntry(t, h); entry.State != dynaUsageUnreachable {
		t.Fatalf("entry = %#v", entry)
	}
	if counter.calls.Load() != 0 {
		t.Fatal("a usage read refreshed the credential")
	}
	live, ok := h.authManager.GetByID("invented")
	if !ok || live.Metadata["access_token"] != "invented-access" || live.Metadata["refresh_token"] != "invented-refresh" {
		t.Fatal("a usage read changed the credential")
	}
	if len(fake.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(fake.requests))
	}
}

func TestDynaUsageResetTimes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reset   time.Time
		seconds int64
		want    bool
	}{
		{"ahead", dynaUsageNow.Add(time.Hour), dynaFiveHourSeconds, true},
		{"past", dynaUsageNow.Add(-time.Hour), dynaFiveHourSeconds, false},
		{"zero", time.Time{}, dynaFiveHourSeconds, false},
		{"beyond the window", dynaUsageNow.Add(30 * time.Hour), dynaFiveHourSeconds, false},
		{"a long calendar month", dynaUsageNow.Add(31 * 24 * time.Hour), dynaMonthSeconds, true},
		{"years ahead with no length", dynaUsageNow.Add(5 * 365 * 24 * time.Hour), 0, false},
	} {
		if got := dynaResetPossible(tc.reset, tc.seconds, dynaUsageNow); got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, got, tc.want)
		}
	}
}
