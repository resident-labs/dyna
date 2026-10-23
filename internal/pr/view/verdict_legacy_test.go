package view_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/pr/state"
	prfixture "dyna-agent/internal/pr/view/testfixture"
	"dyna-agent/internal/pr/watch"
)

// The dyna builds before PR #345 send a text verdict to RecordPRVerdict. A
// session that still runs one of those builds must record its review after the
// engine self-updates, so the old call maps onto one typed result.
func TestLegacyVerdictRecordsOneTypedResult(t *testing.T) {
	for _, tc := range []struct {
		name     string
		verdict  string
		findings []watch.Finding
		want     state.ReviewVerdict
	}{
		{name: "approve", verdict: "approve", want: state.VerdictApprove},
		{name: "approve with nits", verdict: "approve-with-nits", findings: []watch.Finding{{Severity: state.FindingNit, File: "a.go", Line: 4, Why: "The name needs a unit", Fix: "Rename it"}}, want: state.VerdictApprove},
		{name: "changes with findings", verdict: "changes", findings: []watch.Finding{{Severity: state.FindingBlocking, File: "b.go", Line: 9, Why: "The bounds check is missing", Fix: "Check the length first"}}, want: state.VerdictChanges},
		{name: "question", verdict: "question", want: state.VerdictChanges},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			rec := e.room("legacy-verdict")
			p := e.svc.PRs.Watcher()
			const key = "github.com/owner/repo#18"
			const reviewer = "ses_20261023-000001-bbbbbbbb"
			r := &watch.Record{Key: key, Host: "github.com", Repo: "owner/repo", Number: 18, RoomID: rec.ID, Kind: "github", State: "open", Watching: true, Author: "author", Reviewer: reviewer, Head: "head", RoundHead: "head", QuotaAccount: "fixture"}
			st, _ := p.Store.Read()
			st.Records[key] = r
			scope := watch.RateLimitScope{Host: r.Host, Account: r.QuotaAccount, Resource: "core"}
			st.RateLimits = map[string]watch.RateLimit{scope.Key(): {PauseUntil: time.Now().Add(time.Hour)}}
			prfixture.SaveState(t, p.Store.Root, st)

			ctx := authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Session: reviewer, Room: rec.ID})
			got, err := e.svc.PRs.RecordPRVerdict(ctx, rec.ID, key, "head", tc.verdict, "Reviewed the change", 0, tc.findings)
			if err != nil || got.Notice != "verdict saved; applies after GitHub refresh" {
				t.Fatalf("legacy verdict %s = %+v, %v", tc.verdict, got, err)
			}
			saved, err := p.Store.Get(key)
			if err != nil || saved.PendingVerdict == nil {
				t.Fatalf("legacy verdict %s saved no pending result: %+v, %v", tc.verdict, saved, err)
			}
			if pending := saved.PendingVerdict; pending.Verdict != tc.want || pending.Summary != "Reviewed the change" || len(pending.Findings) != len(tc.findings) {
				t.Fatalf("legacy verdict %s saved %+v, want verdict %s with %d findings", tc.verdict, pending, tc.want, len(tc.findings))
			}
		})
	}
}

// A verdict the typed result cannot express is rejected, and an older dyna
// sees the same line it saw before.
func TestLegacyVerdictRejectsUnknownVerdict(t *testing.T) {
	e := newEnv(t)
	rec := e.room("legacy-verdict-rejected")
	p := e.svc.PRs.Watcher()
	const key = "github.com/owner/repo#19"
	const reviewer = "ses_20261023-000002-cccccccc"
	st, _ := p.Store.Read()
	st.Records[key] = &watch.Record{Key: key, Host: "github.com", Repo: "owner/repo", Number: 19, RoomID: rec.ID, Kind: "github", State: "open", Watching: true, Author: "author", Reviewer: reviewer, Head: "head", RoundHead: "head", QuotaAccount: "fixture"}
	prfixture.SaveState(t, p.Store.Root, st)

	ctx := authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Session: reviewer, Room: rec.ID})
	got, err := e.svc.PRs.RecordPRVerdict(ctx, rec.ID, key, "head", "lgtm", "Reviewed the change", 0, nil)
	if err == nil || !strings.Contains(err.Error(), "verdict must be approve, approve-with-nits, changes, or question") {
		t.Fatalf("unknown legacy verdict = %+v, %v", got, err)
	}
	if got.Notice != "" {
		t.Fatalf("unknown legacy verdict returned notice %q", got.Notice)
	}
}
