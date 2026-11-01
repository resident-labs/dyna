package management

import (
	"net/http"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestDynaUsageOpenCodeWindows(t *testing.T) {
	usage := dynaParse(t, parseOpenCodeUsage, `{"usage":{
		"rolling":{"status":"ok","percent":12,"resetsAt":"2026-10-06T14:00:00Z"},
		"weekly":{"status":"ok","percent":0,"resetsAt":"2026-10-13T12:00:00Z"},
		"monthly":{"status":"ok","percent":40.5,"resetsAt":"2026-10-22T00:00:00Z"}}}`)
	if rolling := dynaWindowNamed(t, usage, "five_hour"); rolling.Utilization != 12 || rolling.ResetsAt != "2026-10-06T14:00:00Z" {
		t.Fatalf("rolling = %#v", rolling)
	}
	if weekly := dynaWindowNamed(t, usage, "seven_day"); weekly.Utilization != 0 || weekly.ResetsAt != "" {
		t.Fatalf("weekly = %#v, want no placeholder reset", weekly)
	}
	if monthly := dynaWindowNamed(t, usage, "monthly"); monthly.Kind != "monthly" || monthly.Utilization != 40.5 {
		t.Fatalf("monthly = %#v", monthly)
	}
}

func TestDynaUsageOpenCodeRateLimitedIsSpent(t *testing.T) {
	usage := dynaParse(t, parseOpenCodeUsage, `{"usage":{"rolling":{"status":"rate-limited","percent":37,"resetsAt":"2026-10-06T14:00:00Z"},"weekly":{"status":"rate-limited"}}}`)
	if dynaWindowNamed(t, usage, "five_hour").Utilization != 100 || dynaWindowNamed(t, usage, "seven_day").Utilization != 100 {
		t.Fatalf("windows = %#v", usage.windows)
	}
}

func TestDynaUsageOpenCodeUnknownShapeIsNotZero(t *testing.T) {
	for _, body := range []string{`{}`, `{"usage":{}}`, `{"usage":{"rolling":{"status":"ok"}}}`, `{"rollingUsage":10,"usagePercent":10}`} {
		if usage := dynaParse(t, parseOpenCodeUsage, body); len(usage.windows) != 0 {
			t.Fatalf("%s produced %#v", body, usage.windows)
		}
	}
}

func TestDynaUsageOpenCodeForbiddenMeansNoGoPlan(t *testing.T) {
	fake := dynaFixedUsage(t, http.StatusForbidden, `{}`)
	auth := &coreauth.Auth{ID: "invented", Provider: "openai-compatible-opencode-go", Attributes: map[string]string{"api_key": "invented-key"}}
	if entry := dynaOneEntry(t, dynaUsageHandler(t, auth)); entry.State != dynaUsageUnknown || entry.Error != "" {
		t.Fatalf("entry = %#v", entry)
	}
	request := fake.requests[0]
	if request.URL.String() != "https://opencode.ai/zen/go/v1/usage" || request.Header.Get("Authorization") != "Bearer invented-key" || request.Header.Get("Accept") != "application/json" {
		t.Fatalf("request = %s %#v", request.URL, request.Header)
	}
	if request.Header.Get("X-Opencode-Org-Id") != "" {
		t.Fatal("sent the Console workspace header")
	}
}
