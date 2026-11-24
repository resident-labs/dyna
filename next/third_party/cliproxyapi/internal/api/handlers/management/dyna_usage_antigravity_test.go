package management

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
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
		if window.Kind != "other" || window.Scope == "" || !slices.Equal(window.Models, []string{model}) || window.Seconds != 0 || window.ResetsAt != reset || window.Utilization != float64(i*75) {
			t.Fatalf("quota for %s = %+v", model, window)
		}
	}
}

func TestDynaAntigravityGroupsLiveQuotaShape(t *testing.T) {
	body, err := os.ReadFile("testdata/antigravity_quota.json")
	if err != nil {
		t.Fatal(err)
	}
	usage := dynaParse(t, parseAntigravityUsage, string(body))
	if len(usage.windows) != 2 {
		t.Fatalf("windows = %+v, want two pools", usage.windows)
	}
	byScope := make(map[string]authFileQuotaWindow)
	for _, w := range usage.windows {
		byScope[w.Scope] = w
	}
	claude := byScope["Claude and GPT-OSS models"]
	if claude.Utilization != 0 || claude.ResetsAt != "2026-10-06T16:58:00Z" ||
		!slices.Equal(claude.Models, []string{"claude-opus-5-5-high", "claude-sonnet-5-5-high", "gpt-oss-120b-medium"}) {
		t.Fatalf("Claude and GPT-OSS pool = %+v", claude)
	}
	gemini := byScope["Gemini models"]
	if gemini.Utilization != 100*(1-float64(0.99058527)) || gemini.ResetsAt != "2026-10-06T16:40:00Z" ||
		!slices.Equal(gemini.Models, []string{"gemini-3.8-flash-tiered", "gemini-3-flash", "gemini-3.1-flash-image", "gemini-3.1-flash-lite", "gemini-3.5-flash-lite"}) {
		t.Fatalf("Gemini pool = %+v", gemini)
	}
}

func TestDynaAntigravityKeepsDivergentAndUnrelatedReadings(t *testing.T) {
	usage := dynaParse(t, parseAntigravityUsage, `{"models":{
		"gemini-4-neon-xhigh":{"quotaInfo":{"remainingFraction":0.991,"resetTime":"2026-10-06T14:00:00Z"}},
		"gemini-4-argon-high":{"quotaInfo":{"remainingFraction":0.992,"resetTime":"2026-10-06T14:00:00Z"}},
		"gemini-4-carbon-high":{"quotaInfo":{"remainingFraction":0.991,"resetTime":"2026-10-06T14:00:00.001Z"}},
		"gemini-3.1-flash-lite":{"quotaInfo":{"remainingFraction":1}},
		"gemini-3.1-flash-image":{"quotaInfo":{"remainingFraction":1}},
		"claude-opus-5-5-high":{"quotaInfo":{"remainingFraction":0.991,"resetTime":"2026-10-06T14:00:00Z"}},
		"gpt-oss-120b-medium":{"quotaInfo":{"remainingFraction":1,"resetTime":"2026-10-06T14:00:00Z"}}
	}}`)
	if len(usage.windows) != 7 {
		t.Fatalf("windows = %+v, want seven separate readings", usage.windows)
	}
	seen := make(map[string]bool)
	for _, w := range usage.windows {
		if seen[w.Name] || len(w.Models) != 1 {
			t.Fatalf("window must have a distinct name and one model: %+v", w)
		}
		seen[w.Name] = true
		if w.Models[0] == "gemini-4-carbon-high" && w.ResetsAt != "2026-10-06T14:00:00.001Z" {
			t.Fatalf("reset precision lost: %+v", w)
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
