package view

import (
	"testing"
	"time"

	"dyna-agent/internal/cihealth"
)

func TestRecentRunsDropsRunsOlderThanTheWindow(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	rows := []cihealth.Run{
		{Token: "new", Started: now.Add(-time.Hour)},
		{Token: "edge", Started: now.Add(-runWindow)},
		{Token: "old", Started: now.Add(-runWindow - time.Second)},
	}
	got := recentRuns(rows, now)
	if len(got) != 2 || got[0].Token != "new" || got[1].Token != "edge" {
		t.Fatalf("recentRuns = %+v, want new and edge", got)
	}
}
