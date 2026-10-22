package service

import (
	"strings"
	"testing"
	"time"
)

func TestUsageAfterResetShowsUncertaintyInsteadOfNumbers(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	reset := now.Add(-time.Minute)
	observed := reset.Add(-time.Hour)
	for _, harness := range []string{"codex", "muse"} {
		for _, numeric := range []Window{{UsedPercent: pct(99)}, {Used: pct(99), Limit: pct(100)}} {
			w := numeric
			w.Label, w.ResetsAt = "Weekly", reset.Format(time.RFC3339)
			a := Account{Harness: harness, ObservedAt: observed, Windows: []Window{w}}
			want := "Weekly: probably reset · last checked " + observed.Local().Format("Mon 3:04 PM")
			if got := windowsSummary(a, now); got != want {
				t.Fatalf("%s summary=%q, want %q", harness, got, want)
			}
			resetWindows(&a, now)
			if a.Windows[0].UsedPercent != nil || a.Windows[0].Used != nil || a.Windows[0].UnavailableReason != "probably reset" {
				t.Fatal("reset projection still has a numeric reading")
			}
			if _, ok := WindowPercent(a.Windows[0]); ok {
				t.Fatal("reset window still has a percentage")
			}
			if numeric.UsedPercent != nil && *numeric.UsedPercent != 99 || numeric.Used != nil && *numeric.Used != 99 {
				t.Fatal("projection changed saved data")
			}
		}
	}
}

func TestUsageResetBoundaryAndFreshObservation(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		observed  time.Time
		reset     string
		uncertain bool
	}{
		{"exact reset", now.Add(-time.Second), now.Format(time.RFC3339), true},
		{"fresh at reset", now, now.Format(time.RFC3339), false},
		{"fresh after reset", now.Add(time.Second), now.Format(time.RFC3339), false},
		{"future reset", now.Add(-time.Hour), now.Add(time.Second).Format(time.RFC3339), false},
		{"invalid reset", now.Add(-time.Hour), "invalid", false},
		{"missing reset", now.Add(-time.Hour), "", false},
		{"unknown observation", time.Time{}, now.Format(time.RFC3339), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := Account{ObservedAt: tc.observed, Windows: []Window{{Label: "Weekly", UsedPercent: pct(99), ResetsAt: tc.reset}}}
			got := windowsSummary(a, now)
			if strings.Contains(got, "probably reset") != tc.uncertain {
				t.Fatal(got)
			}
			if !tc.uncertain && !strings.Contains(got, "99%") {
				t.Fatal("fresh reading hidden", got)
			}
		})
	}
}
