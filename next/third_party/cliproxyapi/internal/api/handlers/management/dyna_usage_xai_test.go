package management

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestDynaUsageXAICreditPercent(t *testing.T) {
	usage := dynaParse(t, parseXAIUsage, `{"config":{"creditUsagePercent":37.5,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-10-05T00:00:00Z","end":"2026-10-12T00:00:00Z"},
		"productUsage":[{"product":"grok-build","percent":30},{"product":"api","percent":7.5}]}}`)
	if len(usage.windows) != 1 {
		t.Fatalf("windows = %#v, want one pool", usage.windows)
	}
	weekly := dynaWindowNamed(t, usage, "seven_day")
	if weekly.Kind != "weekly" || weekly.Utilization != 37.5 || weekly.ResetsAt != "2026-10-12T00:00:00Z" {
		t.Fatalf("weekly = %#v", weekly)
	}
}

func TestDynaUsageXAIDeprecatedCounters(t *testing.T) {
	usage := dynaParse(t, parseXAIUsage, `{"config":{"monthlyLimit":{"val":5000},"used":{"val":1250},"billingPeriodEnd":"2026-10-22T00:00:00Z"}}`)
	if monthly := dynaWindowNamed(t, usage, "monthly"); monthly.Utilization != 25 || monthly.ResetsAt != "2026-10-22T00:00:00Z" {
		t.Fatalf("monthly = %#v", monthly)
	}
	usage = dynaParse(t, parseXAIUsage, `{"config":{"monthlyLimit":{"val":5000},"used":{}}}`)
	if monthly := dynaWindowNamed(t, usage, "monthly"); monthly.Utilization != 0 {
		t.Fatalf("a proto3 zero cent = %#v", monthly)
	}
}

func TestDynaUsageXAIMissingPercent(t *testing.T) {
	complete := `{"config":{"currentPeriod":{"type":"USAGE_PERIOD_TYPE_MONTHLY","start":"2026-10-01T00:00:00Z","end":"2026-11-01T00:00:00Z"}}}`
	if monthly := dynaWindowNamed(t, dynaParse(t, parseXAIUsage, complete), "monthly"); monthly.Utilization != 0 {
		t.Fatalf("monthly = %#v", monthly)
	}
	for _, body := range []string{
		`{}`,
		`{"config":null}`,
		`{"config":{}}`,
		`{"config":{"monthlyLimit":{"val":5000}}}`,
		`{"config":{"currentPeriod":{"type":"USAGE_PERIOD_TYPE_UNSPECIFIED","start":"2026-10-01T00:00:00Z","end":"2026-11-01T00:00:00Z"}}}`,
		`{"config":{"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","end":"2026-10-12T00:00:00Z"}}}`,
		`{"config":{"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-10-12T00:00:00Z","end":"2026-10-05T00:00:00Z"}}}`,
		`{"config":{"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-02-30T00:00:00Z","end":"2026-03-05T00:00:00Z"}}}`,
	} {
		if usage := dynaParse(t, parseXAIUsage, body); len(usage.windows) != 0 {
			t.Fatalf("%s produced %#v", body, usage.windows)
		}
	}
}

func TestDynaUsageXAIRequestHeaders(t *testing.T) {
	fake := dynaFixedUsage(t, http.StatusOK, `{"config":{"creditUsagePercent":1}}`)
	entry := dynaOneEntry(t, dynaUsageHandler(t, &coreauth.Auth{ID: "invented", Provider: "xai", Metadata: map[string]any{"access_token": "invented-access", "sub": "invented-subject"}}))
	if entry.State != dynaUsageOK || len(entry.Windows) != 1 || entry.Windows[0].Kind != "other" {
		t.Fatalf("entry = %#v", entry)
	}
	request := fake.requests[0]
	want := map[string]string{"Authorization": "Bearer invented-access", "X-Xai-Token-Auth": "xai-grok-cli", "X-Grok-Client-Version": helps.GetXAIClientVersion(), "X-Userid": "invented-subject"}
	for name, value := range want {
		if got := request.Header.Get(name); got != value {
			t.Fatalf("%s = %q, want %q", name, got, value)
		}
	}
	if request.URL.String() != "https://cli-chat-proxy.grok.com/v1/billing?format=credits" {
		t.Fatalf("url = %s", request.URL)
	}
}
