package service

import (
	"strings"
	"testing"
	"time"
)

func pct(v float64) *float64 { return &v }

func TestUsageNote(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	reset := now.Add(30 * time.Hour).Format(time.RFC3339)
	acct := func(observed time.Time, windows ...Window) Account {
		return Account{Harness: "claude-code", Label: "me@example.com", ObservedAt: observed, Windows: windows}
	}
	cases := []struct {
		name string
		in   Account
		want string // substring; "" means no note
	}{
		{"quiet", acct(now, Window{Label: "5-hour", UsedPercent: pct(12)}, Window{Label: "weekly", UsedPercent: pct(41)}), ""},
		{"busy", acct(now, Window{Label: "5-hour", UsedPercent: pct(12)}, Window{Label: "weekly", UsedPercent: pct(50), ResetsAt: reset}),
			"[DYNA USAGE] claude-code (me@example.com): 5-hour 12% · weekly 50%, resets " + now.Add(30*time.Hour).Local().Format("Mon 15:04")},
		{"over 80%", acct(now, Window{Label: "weekly", UsedPercent: pct(81)}), "weekly 81%. Use a cheaper profile and check `dyna usage` before large fan-outs."},
		{"fraction", acct(now, Window{Label: "monthly", Used: pct(90), Limit: pct(100)}), "monthly 90%"},
		{"stale", acct(now.Add(-2*time.Hour), Window{Label: "5-hour", UsedPercent: pct(5)}), "reading 2 h old"},
		{"never read", acct(time.Time{}), "no reading yet"},
	}
	for _, c := range cases {
		got := UsageNote(c.in, now)
		if c.want == "" && got != "" || !strings.Contains(got, c.want) {
			t.Errorf("%s: UsageNote = %q, want %q", c.name, got, c.want)
		}
	}
}
