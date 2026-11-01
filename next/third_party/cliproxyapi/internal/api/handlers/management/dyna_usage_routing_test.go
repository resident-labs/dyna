package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const dynaRoutingCodexUsage = `{"rate_limit":{"primary_window":{"used_percent":20,"limit_window_seconds":18000,"reset_after_seconds":3600},"secondary_window":{"used_percent":40,"limit_window_seconds":604800,"reset_after_seconds":86400}}}`

func dynaRoutingCodex(id string) *coreauth.Auth {
	return &coreauth.Auth{ID: id, Provider: "codex", Metadata: map[string]any{"access_token": "invented-token", "account_id": "invented-account"}}
}

func dynaRoutingCalls(fake *dynaFakeUsage) int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return len(fake.requests)
}

func TestDynaQuotaHintClaudeWindows(t *testing.T) {
	entry := authFileQuotaEntry{ObservedAt: "2026-11-01T12:00:00Z", StatusCode: http.StatusOK, Windows: []authFileQuotaWindow{
		{Kind: "session", Utilization: 30, ResetsAt: "2026-11-01T14:00:00Z"},
		{Kind: "weekly_all", Utilization: 10, ResetsAt: "2026-11-05T00:00:00Z"},
		{Kind: "weekly_scoped", Scope: "Oauth Apps", Utilization: 56, ResetsAt: "2026-11-01T15:00:00Z"},
		{Kind: "weekly_scoped", Scope: "Fable", Utilization: 100, ResetsAt: "0001-01-01T00:00:00Z"},
	}}
	hint, ok := dynaQuotaHint(entry)
	if !ok || len(hint.Windows) != 3 {
		t.Fatalf("hint = %+v, %v", hint, ok)
	}
	byName := make(map[string]coreauth.QuotaResetWindow)
	for _, window := range hint.Windows {
		byName[window.Model] = window
		if window.Model == "" && window.Seconds == dynaFiveHourSeconds {
			byName["five"] = window
		}
	}
	if byName["five"].UsedPercent != 30 {
		t.Fatalf("five hour = %+v", byName["five"])
	}
	// The engine shows the OAuth Apps window as the weekly one, so routing must rank by it.
	for _, window := range hint.Windows {
		if window.Model == "" && window.Seconds == dynaWeekSeconds && window.UsedPercent != 56 {
			t.Fatalf("weekly = %+v", window)
		}
	}
	if fable := byName["Fable"]; fable.UsedPercent != 100 || !fable.ResetAt.IsZero() {
		t.Fatalf("Go's zero time should read as no reset: %+v", fable)
	}
}

func TestDynaQuotaHintFailuresPublishNothing(t *testing.T) {
	for name, entry := range map[string]authFileQuotaEntry{
		"claude failed": {ObservedAt: "2026-11-01T12:00:00Z", StatusCode: http.StatusTooManyRequests, Error: "upstream returned status 429"},
		"rate limited":  {ObservedAt: "2026-11-01T12:00:00Z", dynaQuotaEntryFields: dynaQuotaEntryFields{State: dynaUsageRateLimited}},
		"unreachable":   {ObservedAt: "2026-11-01T12:00:00Z", dynaQuotaEntryFields: dynaQuotaEntryFields{State: dynaUsageUnreachable}},
		"unknown":       {ObservedAt: "2026-11-01T12:00:00Z", dynaQuotaEntryFields: dynaQuotaEntryFields{State: dynaUsageUnknown}},
	} {
		if hint, ok := dynaQuotaHint(entry); ok {
			t.Errorf("%s published %+v", name, hint)
		}
	}
}

func TestDynaQuotaHintBalances(t *testing.T) {
	read := func(amounts ...string) coreauth.QuotaResetHint {
		entry := authFileQuotaEntry{ObservedAt: "2026-11-01T12:00:00Z", dynaQuotaEntryFields: dynaQuotaEntryFields{State: dynaUsageOK}}
		for _, amount := range amounts {
			entry.Balances = append(entry.Balances, dynaQuotaBalance{Currency: "USD", Amount: amount})
		}
		hint, ok := dynaQuotaHint(entry)
		if !ok {
			t.Fatal("no hint")
		}
		return hint
	}
	if hint := read("3.50"); !hint.Prepaid || hint.Empty {
		t.Fatalf("credit: %+v", hint)
	}
	if hint := read("0.00", "-0.01"); !hint.Prepaid || !hint.Empty {
		t.Fatalf("empty: %+v", hint)
	}
	if hint := read("0.00", "2.00"); hint.Empty {
		t.Fatalf("one balance still has credit: %+v", hint)
	}
}

func TestHomeReadPublishesToRouting(t *testing.T) {
	fake := dynaFixedUsage(t, http.StatusOK, dynaRoutingCodexUsage)
	handler := dynaUsageHandler(t, dynaRoutingCodex("codex-1"))
	handler.authManager.SetSelector(coreauth.NewSessionAffinitySelector(&coreauth.SoonestResetSelector{}))
	runAuthFileQuota(t, handler, "")
	views := handler.authManager.QuotaRoutingSnapshot().Providers["codex"]
	if len(views) != 1 || views[0].Deadline == nil {
		t.Fatalf("routing did not see the read: %+v", views)
	}
	if dynaRoutingCalls(fake) != 1 {
		t.Fatalf("requests = %d", dynaRoutingCalls(fake))
	}
}

func TestReadsShareOneUpstreamRequest(t *testing.T) {
	var started atomic.Int32
	release := make(chan struct{})
	fake := &dynaFakeUsage{respond: func(*http.Request) (*http.Response, error) {
		started.Add(1)
		<-release
		return dynaResponse(http.StatusOK, dynaRoutingCodexUsage), nil
	}}
	dynaUseTransport(t, fake)
	handler := dynaUsageHandler(t, dynaRoutingCodex("codex-1"))
	auth := handler.authManager.List()[0]
	var reads sync.WaitGroup
	for range 16 {
		reads.Go(func() { handler.sharedQuota(context.Background(), handler.cfg, handler.authManager, auth) })
	}
	for started.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	close(release)
	reads.Wait()
	handler.sharedQuota(context.Background(), handler.cfg, handler.authManager, auth)
	if got := dynaRoutingCalls(fake); got != 1 {
		t.Fatalf("requests = %d", got)
	}
}

func TestRefreshReusesARecentHomeRead(t *testing.T) {
	fake := dynaFixedUsage(t, http.StatusOK, dynaRoutingCodexUsage)
	handler := dynaUsageHandler(t, dynaRoutingCodex("codex-1"))
	runAuthFileQuota(t, handler, "")
	if handler.refreshDynaUsage("codex") {
		t.Fatal("reported a rate limit")
	}
	if got := dynaRoutingCalls(fake); got != 1 {
		t.Fatalf("requests = %d", got)
	}
}

func TestRefreshReadsOnlyItsProviderAndReportsARateLimit(t *testing.T) {
	fake := dynaFixedUsage(t, http.StatusTooManyRequests, `{}`)
	disabled := dynaRoutingCodex("codex-off")
	disabled.Disabled = true
	other := &coreauth.Auth{ID: "kimi-1", Provider: "kimi", Metadata: map[string]any{"access_token": "invented-token"}}
	handler := dynaUsageHandler(t, dynaRoutingCodex("codex-1"), disabled, other)
	if !handler.refreshDynaUsage("codex") {
		t.Fatal("a 429 was not reported")
	}
	if got := dynaRoutingCalls(fake); got != 1 {
		t.Fatalf("requests = %d", got)
	}
}

func TestFailedReadKeepsTheLastReading(t *testing.T) {
	fake := dynaFixedUsage(t, http.StatusOK, dynaRoutingCodexUsage)
	handler := dynaUsageHandler(t, dynaRoutingCodex("codex-1"))
	handler.authManager.SetSelector(coreauth.NewSessionAffinitySelector(&coreauth.SoonestResetSelector{}))
	runAuthFileQuota(t, handler, "")
	handler.dynaUsage.mu.Lock()
	handler.dynaUsage.entries = nil
	handler.dynaUsage.mu.Unlock()
	fake.respond = func(*http.Request) (*http.Response, error) {
		return dynaResponse(http.StatusTooManyRequests, `{}`), nil
	}
	runAuthFileQuota(t, handler, "")
	if views := handler.authManager.QuotaRoutingSnapshot().Providers["codex"]; len(views) != 1 || views[0].Deadline == nil {
		t.Fatalf("a failed read dropped the reading: %+v", views)
	}
}

func TestGetRoutingStatusNamesNoCredential(t *testing.T) {
	dynaFixedUsage(t, http.StatusOK, dynaRoutingCodexUsage)
	handler := dynaUsageHandler(t, dynaRoutingCodex("codex-1"))
	handler.authManager.SetSelector(coreauth.NewSessionAffinitySelector(&coreauth.SoonestResetSelector{}))
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/v8/management/routing/status", nil)
	handler.GetRoutingStatus(context)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var snapshot coreauth.QuotaRoutingSnapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Strategy != "soonest-reset" || len(snapshot.Providers["codex"]) != 1 {
		t.Fatalf("snapshot = %s", recorder.Body.String())
	}
	if body := recorder.Body.String(); len(snapshot.Providers["codex"][0].AuthIndex) == 0 || strings.Contains(body, "codex-1") || strings.Contains(body, "invented") {
		t.Fatalf("snapshot names a credential: %s", body)
	}
}

func TestNormalizeRoutingStrategyAcceptsSoonestReset(t *testing.T) {
	for _, value := range []string{"soonest-reset", "SoonestReset", " soonest-reset-first "} {
		if got, ok := normalizeRoutingStrategy(value); !ok || got != "soonest-reset" {
			t.Errorf("%q = %q, %v", value, got, ok)
		}
	}
}
