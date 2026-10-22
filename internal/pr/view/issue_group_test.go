package view

import (
	"testing"
	"time"
)

func TestIssueGroup(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.FixedZone("local", -5*3600))
	today, yesterday := now.Add(-time.Hour), now.Add(-24*time.Hour)
	utcTodayLocalYesterday := now.Add(-13 * time.Hour).UTC()
	open := []GitHubLinkedPR{{Number: 7, State: "OPEN", IsDraft: true}}
	for _, tc := range []struct {
		name, state string
		closed      *time.Time
		prs         []GitHubLinkedPR
		live        bool
		want        string
	}{
		{"closed today wins", "CLOSED", &today, open, true, "closed-today"},
		{"UTC date uses local day", "CLOSED", &utcTodayLocalYesterday, nil, false, ""},
		{"old closed has no group", "CLOSED", &yesterday, open, true, ""},
		{"open PR wins", "OPEN", nil, open, true, "has-pr"},
		{"draft PR counts", "OPEN", nil, open, false, "has-pr"},
		{"live agent", "OPEN", nil, []GitHubLinkedPR{{State: "MERGED"}}, true, "working"},
		{"finished agent", "OPEN", nil, nil, false, "needs-owner"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			it := GitHubItem{State: tc.state, ClosedAt: tc.closed, LinkedPRs: tc.prs, WorkingSessionLive: tc.live}
			if got := IssueGroup(it, now); got != tc.want {
				t.Fatalf("group = %q, want %q", got, tc.want)
			}
		})
	}
}
