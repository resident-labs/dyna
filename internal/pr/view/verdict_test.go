package view_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	prfixture "dyna-agent/internal/pr/view/testfixture"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

func TestVerdictUsesAuthenticatedReviewerAndPreservesPendingNotice(t *testing.T) {
	e := newEnv(t)
	alpha, beta := e.room("verdict-alpha"), e.room("verdict-beta")
	p := e.svc.PRs.Watcher()
	const key = "github.com/owner/repo#17"
	const reviewer = "ses_20261017-000001-aaaaaaaa"
	r := &watch.Record{Key: key, Host: "github.com", Repo: "owner/repo", Number: 17, RoomID: alpha.ID, Kind: "github", State: "open", Watching: true, Author: "author", Reviewer: reviewer, Head: "head", RoundHead: "head", QuotaAccount: "fixture"}
	st, _ := p.Store.Read()
	st.Records[key] = r
	scope := watch.RateLimitScope{Host: r.Host, Account: r.QuotaAccount, Resource: "core"}
	st.RateLimits = map[string]watch.RateLimit{scope.Key(): {PauseUntil: time.Now().Add(time.Hour)}}
	prfixture.SaveState(t, p.Store.Root, st)
	for _, tc := range []struct {
		actor   authority.Actor
		allowed bool
	}{
		{authority.Actor{Role: authority.Session, Session: reviewer, Room: alpha.ID}, true},
		{authority.Actor{Role: authority.Root, Session: reviewer, Room: alpha.ID}, true},
		{authority.Actor{Role: authority.Session, Session: "another-session", Room: alpha.ID}, false},
		{authority.Actor{Role: authority.Root, Session: reviewer, Room: beta.ID}, false},
		{authority.Actor{Role: authority.Worker, Session: reviewer, Run: "run", Room: alpha.ID}, false},
		{authority.Actor{Role: authority.Owner}, false},
		{authority.Actor{Room: alpha.ID, Session: reviewer}, false},
	} {
		ctx := authority.WithActor(context.Background(), tc.actor)
		got, err := e.svc.PRs.RecordPRVerdict(ctx, alpha.ID, key, "head", "approve", "Reviewed", 0, nil)
		if tc.allowed {
			if err != nil || got.Notice != "verdict saved; applies after GitHub refresh" {
				t.Errorf("verdict as %+v = %+v, %v", tc.actor, got, err)
			}
			saved, err := p.Store.Get(key)
			if err != nil || saved.PendingVerdict == nil || saved.PendingVerdict.Actor != reviewer || saved.Verdict != "" {
				t.Fatalf("pending caller = %+v, %v", saved.PendingVerdict, err)
			}
		} else if err == nil {
			t.Errorf("verdict as %+v was accepted: %+v", tc.actor, got)
		}
	}
}

func TestVerdictPreservesStaleNoticeAndValidationErrors(t *testing.T) {
	e := newEnv(t)
	rec := e.room("stale-verdict")
	p := e.svc.PRs.Watcher()
	const reviewer = "ses_20261017-000001-aaaaaaaa"
	st, _ := p.Store.Read()
	st.Records["change"] = &watch.Record{Key: "change", RoomID: rec.ID, Kind: "local", State: "merged", Head: "head", Author: "author", LastReviewer: reviewer, LastReviewHead: "head"}
	prfixture.SaveState(t, p.Store.Root, st)
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: reviewer, Room: rec.ID})
	got, err := e.svc.PRs.RecordPRVerdict(ctx, rec.ID, "change", "head", "approve", "Reviewed", 0, nil)
	if err != nil || !strings.Contains(got.Notice, "already merged; no verdict needed") {
		t.Fatalf("stale result = %+v, %v", got, err)
	}
	got, err = e.svc.PRs.RecordPRVerdict(ctx, rec.ID, "change", "head", "invalid", "Reviewed", 0, nil)
	if err == nil || got.Notice != "" || !strings.Contains(err.Error(), "verdict must be") {
		t.Fatalf("validation result = %+v, %v", got, err)
	}
}

func TestNamedVerdictUsesAuthenticatedSessionWithoutAssignment(t *testing.T) {
	e := newEnv(t)
	rec := e.room("named-verdict")
	p := e.svc.PRs.Watcher()
	const key = "github.com/owner/repo#17"
	const reviewer = "ses_20261017-000005-eeeeeeee"
	recordtest.Write(t, p.Notify.Sessions.Store.Root, reviewer, subagent.Meta{ID: reviewer, Name: "arch-review", RoomID: rec.ID, Room: rec.Name})
	st, _ := p.Store.Read()
	r := &watch.Record{Key: key, Host: "github.com", Repo: "owner/repo", Number: 17, RoomID: rec.ID, Kind: "github", State: "open", Watching: true, Author: "author", Head: "head", QuotaAccount: "fixture"}
	st.Records[key] = r
	scope := watch.RateLimitScope{Host: r.Host, Account: r.QuotaAccount, Resource: "core"}
	st.RateLimits = map[string]watch.RateLimit{scope.Key(): {PauseUntil: time.Now().Add(time.Hour)}}
	prfixture.SaveState(t, p.Store.Root, st)
	policy, err := e.svc.PRs.RoomChangePolicy(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	policy.Review, policy.Reviewers = "off", []string{reviewer}
	owner := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	if err = e.svc.PRs.SetRoomChangePolicy(owner, rec.ID, policy); err != nil {
		t.Fatal(err)
	}
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: reviewer, Room: rec.ID})
	got, err := e.svc.PRs.RecordPRVerdict(ctx, rec.ID, key, "head", "approve", "Reviewed exact head", 0, nil)
	if err != nil || got.Notice != "verdict saved; applies after GitHub refresh" {
		t.Fatalf("named verdict = %+v, %v", got, err)
	}
	saved, err := p.Store.Get(key)
	if err != nil || saved.Reviewer != "" || saved.PendingVerdict == nil || saved.PendingVerdict.Actor != reviewer || saved.PendingVerdict.Head != "head" {
		t.Fatalf("named caller was not preserved: %+v, %v", saved, err)
	}
}
