package management

import (
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestDynaAntigravityReportsModelFractionsAndResets(t *testing.T) {
	reset := time.Now().Add(time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
	dynaUseTransport(t, &dynaFakeUsage{respond: func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil || r.Method != http.MethodPost || r.URL.String() != "https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels" || string(body) != `{"project":"invented-project"}` {
			t.Error("quota request did not match the Antigravity entitlement endpoint")
		}
		if r.Header.Get("Authorization") != "Bearer invented-access" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("missing Antigravity quota request headers")
		}
		return dynaResponse(http.StatusOK, fmt.Sprintf(`{"models":{
			"gemini-4-neon-xhigh":{"quotaInfo":{"remainingFraction":0.25,"resetTime":%q}},
			"claude-opus-5-5-high":{"quotaInfo":{"remainingFraction":1,"resetTime":%q}}
		}}`, reset, reset)), nil
	}})
	h := dynaUsageHandler(t, &coreauth.Auth{ID: "invented", Provider: "antigravity", Metadata: map[string]any{
		"access_token": "invented-access", "project_id": "invented-project",
	}})
	entry := dynaOneEntry(t, h)
	if entry.State != dynaUsageOK || len(entry.Windows) != 2 {
		t.Fatalf("quota = %+v, want two model readings", entry)
	}
	for i, model := range []string{"claude-opus-5-5-high", "gemini-4-neon-xhigh"} {
		window := entry.Windows[i]
		if window.Kind != "other" || window.Scope != "antigravity/"+model || window.Seconds != 0 || window.ResetsAt != reset || window.Utilization != float64(i*75) {
			t.Fatalf("quota for %s = %+v", model, window)
		}
	}
}

func TestDynaAntigravityLeavesInvalidAndExpiredReadingsUnknown(t *testing.T) {
	usage := dynaParse(t, parseAntigravityUsage, `{"models":{
		"gemini-4-neon-xhigh":{"quotaInfo":{"remainingFraction":0,"resetTime":"2026-10-06T10:00:00Z"}},
		"gemini-4-argon-high":{"quotaInfo":{"resetTime":"2026-10-06T15:00:00Z"}},
		"gemini-4-carbon-high":{"quotaInfo":{"remainingFraction":2}},
		"claude-opus-5-5-high":{"quotaInfo":{"remainingFraction":"NaN"}},
		"not-in-catalog":{"quotaInfo":{"remainingFraction":0.8}}
	}}`)
	if len(usage.windows) != 0 {
		t.Fatalf("invalid quota was surfaced: %+v", usage.windows)
	}
}
