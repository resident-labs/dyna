package management

import (
	"os"
	"slices"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Google's Gemini reaches Dyna only through agy, so nothing polls an Antigravity credential's quota.
func TestDynaAntigravityIsNeverPolled(t *testing.T) {
	if _, ok := dynaUsageSources["antigravity"]; ok {
		t.Fatal("antigravity has a usage source")
	}
	if _, ok := dynaUsageSourceFor(&coreauth.Auth{Provider: "antigravity"}); ok {
		t.Fatal("an antigravity credential has a usage reader")
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
