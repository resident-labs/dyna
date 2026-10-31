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

func TestRunPhasesPreserveTotalsAndHideRequestValues(t *testing.T) {
	now := time.Unix(1, 0)
	var lines []string
	q := &quotaLog{now: func() time.Time { return now }, logf: func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }}
	caller := ghQuotaCaller("repos/private/repository/actions/runs?head_sha=secret")
	q.recordPhase(caller, RunWatchSnapshot, false)
	q.recordPhase(caller, RunCancellation, false)
	q.recordPhase(caller, RunOther, true)
	now = now.Add(window)
	q.recordPhase("graphql", RunOther, false)
	if len(lines) != 2 || !strings.Contains(lines[0], "runs-head-sha 2") ||
		!strings.Contains(lines[1], "snapshot 1 counted 0 free; cancellation 1 counted 0 free") ||
		!strings.Contains(lines[1], "other 0 counted 1 free") ||
		strings.Contains(strings.Join(lines, " "), "secret") || strings.Contains(strings.Join(lines, " "), "private") {
		t.Fatalf("unexpected quota summaries: %v", lines)
	}
}

// A workflow run list is named by the filter it asks for, so a per-commit read
// is told apart from a branch cleanup. Filter values never reach the label.
func TestRunListCallerNamesTheFilterNotItsValue(t *testing.T) {
	cases := map[string]string{
		"repos/owner/repo/actions/runs?head_sha=abc123&per_page=100":      "runs-head-sha",
		"repos/owner/repo/actions/runs?branch=feature%2Fx&status=success": "runs-branch",
		"repos/owner/repo/actions/runs?event=push&per_page=100":           "runs-branch",
		"repos/owner/repo/actions/runs?per_page=100":                      "runs",
	}
	for path, want := range cases {
		if got := ghQuotaCaller(path); got != want {
			t.Errorf("ghQuotaCaller(%q) = %q, want %q", path, got, want)
		}
	}
}

// Known endpoints get a stable label, and only a truly unknown path stays other.
// Commit SHAs and artifact IDs in the path never reach the label.
func TestGHQuotaCallerLabelsKnownEndpointsNotOther(t *testing.T) {
	cases := map[string]string{
		"repos/owner/repo/issues/12":                     "issue-detail",
		"repos/owner/repo/issues/12/events?per_page=100": "issue-events",
		"repos/owner/repo/issues/12/comments?since=x":    "issue-comments",
		"repos/owner/repo/compare/aaaa1111...bbbb2222":   "compare",
		"repos/owner/repo/actions/artifacts/4242/zip":    "artifact-zip",
		"repos/owner/repo/git/refs/heads/feature":        "other",
	}
	for path, want := range cases {
		if got := ghQuotaCaller(path); got != want {
			t.Errorf("ghQuotaCaller(%q) = %q, want %q", path, got, want)
		}
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
