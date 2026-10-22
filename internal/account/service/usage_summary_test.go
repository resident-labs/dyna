package service

import (
	"strings"
	"testing"
	"time"
)

func TestSummaryNeverShowsAPastResetTime(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	past := now.Add(-49 * time.Hour).Format(time.RFC3339)
	next := now.Add(3 * time.Hour).Format(time.RFC3339)
	percent := 10.0
	old := Account{ObservedAt: now.Add(-72 * time.Hour), Windows: []Window{{Label: "Weekly", UsedPercent: &percent, ResetsAt: past}}}
	got := windowsSummary(old, now)
	if !strings.Contains(got, "probably reset · last checked") || strings.Contains(got, past) {
		t.Fatalf("past reset shown: %q", got)
	}
	mixed := Account{ObservedAt: now.Add(-72 * time.Hour), Windows: []Window{{Label: "5-hour", UsedPercent: &percent, ResetsAt: next}, {Label: "Weekly", UsedPercent: &percent, ResetsAt: past}}}
	if got = windowsSummary(mixed, now); strings.Contains(got, "unknown") || !strings.Contains(got, "resets ") {
		t.Fatalf("a future reset must win over a past one: %q", got)
	}
}
