package ghquota

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// The engine shares one REST quota with every agent, so the journal must name
// the callers that spend it, and keep 304 replies apart from counted requests.
func TestQuotaSummaryNamesTheCallersThatSpendQuota(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	var lines []string
	q := &quotaLog{
		now:  func() time.Time { return now },
		logf: func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) },
	}
	q.record(ghQuotaCaller("repos/owner/repo/pulls/17/comments?per_page=100"), true)
	q.record(ghQuotaCaller("repos/owner/repo/pulls/17/comments?per_page=100"), false)
	q.record(ghQuotaCaller("repos/owner/repo/actions/runs/99/attempts/1/jobs?per_page=100"), false)
	q.record(ghQuotaCaller("graphql"), false)
	if len(lines) != 0 {
		t.Fatalf("summary before the window closed: %v", lines)
	}
	now = now.Add(window)
	q.record(ghQuotaCaller("graphql"), true)
	if len(lines) != 1 {
		t.Fatalf("summaries = %v, want one", lines)
	}
	want := "3 counted and 2 free (304) requests in 10m0s; counted by caller: comments 1, graphql 1, run-jobs 1"
	if !strings.Contains(lines[0], want) {
		t.Fatalf("summary = %q, want it to contain %q", lines[0], want)
	}
}

// A caller with no API path, such as a session's access check, is counted
// under its own name so the journal shows what turn startup spends.
func TestQuotaSummaryCountsANamedCaller(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	var lines []string
	q := &quotaLog{
		now:  func() time.Time { return now },
		logf: func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) },
	}
	q.record("access-check", false)
	now = now.Add(window)
	q.record("access-check", false)
	if len(lines) != 1 || !strings.Contains(lines[0], "counted by caller: access-check 2") {
		t.Fatalf("summaries = %v, want the named caller counted", lines)
	}
}
