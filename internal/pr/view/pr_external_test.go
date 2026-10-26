package view_test

import (
	"dyna-agent/internal/mode"
	prview "dyna-agent/internal/pr/view"
	prfixture "dyna-agent/internal/pr/view/testfixture"
	"dyna-agent/internal/pr/watch"
	"reflect"
	"strings"
	"testing"
)

func TestReviewerProfileClearsThroughEitherSettingsAPI(t *testing.T) {
	for _, method := range []string{"settings", "reviewer"} {
		t.Run(method, func(t *testing.T) {
			e := newEnv(t)
			rec := e.room("reviewer-settings")
			if _, err := e.svc.PRs.SetPRSettings(ownerCtx, rec.ID, prview.PRConfig{ReviewerProfile: "mock", ReviewPublishing: watch.PublishingGitHub}); err != nil {
				t.Fatal(err)
			}
			if method == "settings" {
				if _, err := e.svc.PRs.SetPRSettings(ownerCtx, rec.ID, prview.PRConfig{ReviewPublishing: watch.PublishingGitHub}); err != nil {
					t.Fatal(err)
				}
			} else if err := e.svc.PRs.SetReviewerProfile(ownerCtx, rec.ID, ""); err != nil {
				t.Fatal(err)
			}
			got, err := e.svc.PRs.PRSettings(rec.ID)
			if err != nil || got.ReviewerProfile != "" || got.ReviewPublishing != watch.PublishingGitHub {
				t.Fatalf("empty profile must select auto and keep publishing: %+v: %v", got, err)
			}
		})
	}
}

func TestReviewPublishingSettingsAndFullLocalRows(t *testing.T) {
	e := newEnv(t)
	rec := e.room("review-room")
	cfg, err := e.svc.PRs.PRSettings(rec.ID)
	if err != nil || cfg.ReviewPublishing != watch.PublishingLocal {
		t.Fatalf("legacy default: %+v, %v", cfg, err)
	}
	for _, value := range []string{watch.PublishingGitHub, watch.PublishingLocal} {
		saved, err := e.svc.PRs.SetPRSettings(ownerCtx, rec.ID, prview.PRConfig{ReviewerProfile: "mock", ReviewPublishing: value})
		if err != nil || saved.ReviewPublishing != value {
			t.Fatalf("save: %+v, %v", saved, err)
		}
		read, err := e.svc.PRs.PRSettings(rec.ID)
		if err != nil || !reflect.DeepEqual(read, saved) {
			t.Fatalf("round trip: %+v, %v", read, err)
		}
	}
	if _, err := e.svc.PRs.SetPRSettings(ownerCtx, rec.ID, prview.PRConfig{ReviewPublishing: "invalid"}); err == nil {
		t.Fatal("invalid setting accepted")
	}
	summary := "## Findings\n\n`internal/parser.go:42`: fix the bound.\n\n" + strings.Repeat("More detail.\n", 200) + "\n`tail.go:99`: last finding."
	const key = "github.com/owner/repo#17"
	st := watch.State{Version: 1, Records: map[string]*watch.Record{key: {Key: key, RoomID: rec.ID, URL: "https://github.com/owner/repo/pull/17", Repo: "owner/repo", Number: 17, State: "open", Watching: true, Verdict: "changes", Summary: summary}}, Reviewers: map[string]watch.Reviewer{}}
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
	for _, roomRef := range []string{rec.ID, ""} {
		rows, err := e.svc.PRs.Changes(roomRef, "", 0)
		if err != nil || len(rows.Items) != 1 || !rows.Items[0].HasReviewSummary || rows.Items[0].ReviewPublishing != watch.PublishingLocal || rows.Items[0].ReviewURL != "" {
			t.Fatalf("local rows: %+v, %v", rows, err)
		}
	}
	detail, err := e.svc.PRs.ChangeDetail(rec.ID, key)
	if err != nil || detail.Summary != summary {
		t.Fatalf("full review detail: %+v %v", detail, err)
	}
	st.Records[key].ReviewID = 88
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
	rows, err := e.svc.PRs.Changes(rec.ID, "", 0)
	if err != nil || rows.Items[0].ReviewPublishing != watch.PublishingGitHub || !strings.Contains(rows.Items[0].ReviewURL, "pullrequestreview-88") {
		t.Fatalf("historical public review mislabeled: %+v, %v", rows, err)
	}
}

func TestChangesIncludeLocalRowsAndEnforceReplyRoom(t *testing.T) {
	e := newEnv(t)
	alpha, beta := e.room("changes-alpha"), e.room("changes-beta")
	// Web mutations act as the owner, independent of the engine's stamp.
	t.Setenv(mode.EnvMode, mode.Subagent)
	t.Setenv(mode.EnvSession, "ses_engine_worker")
	t.Setenv(mode.EnvRoomID, beta.ID)
	st := watch.State{Version: 1, Records: map[string]*watch.Record{
		"local": {Key: "local", ChangeID: "change_local", Kind: "local", RoomID: alpha.ID, Repo: "owner/repo", Branch: "feature", Title: "Local fix", Head: "head", Author: "author", State: "open", Watching: true, Verdict: "changes", Findings: []watch.Finding{{ID: "bounds", Severity: "blocking", File: "code.go", Line: 1, Why: "Bounds", Fix: "Check"}}},
		"pr":    {Key: "pr", Number: 17, RoomID: alpha.ID, Repo: "owner/repo", State: "open", Watching: true},
	}, Reviewers: map[string]watch.Reviewer{}}
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
	changes, err := e.svc.PRs.Changes(alpha.ID, "", 0)
	if err != nil || len(changes.Items) != 2 {
		t.Fatalf("changes: %+v %v", changes, err)
	}
	if err = e.svc.PRs.ReplyToFinding(ownerCtx, beta.ID, "change_local", "bounds", "fixed", "Done"); err == nil {
		t.Fatal("reply crossed room boundary")
	}
	if err = e.svc.PRs.ReplyToFinding(ownerCtx, alpha.ID, "change_local", "bounds", "fixed", "Added the check"); err != nil {
		t.Fatal(err)
	}
	detail, err := e.svc.PRs.ChangeDetail(alpha.ID, "local")
	if err != nil || detail.Verdict != "" || len(detail.Findings[0].Replies) != 1 || detail.ReviewState != "queued" {
		t.Fatalf("reply detail: %+v %v", detail, err)
	}
	if _, err := e.svc.PRs.ChangeDetail(beta.ID, "local"); err == nil {
		t.Fatal("detail crossed room boundary")
	}
}

func TestRoomMergeStatusListsOnlyThatRoomsPRs(t *testing.T) {
	e := newEnv(t)
	alpha, beta := e.room("merge-alpha"), e.room("merge-beta")
	if _, err := e.svc.PRs.SetPRSettings(ownerCtx, alpha.ID, prview.PRConfig{GitHubAccess: "read-write"}); err != nil {
		t.Fatal(err)
	}
	st := watch.State{Version: 1, Records: map[string]*watch.Record{
		"a": {Key: "a", ChangeID: "a", Kind: "github", Number: 1, RoomID: alpha.ID, Repo: "owner/repo", Head: "h1", State: "open", Watching: true},
		"b": {Key: "b", ChangeID: "b", Kind: "github", Number: 2, RoomID: beta.ID, Repo: "owner/repo", Head: "h2", State: "open", Watching: true},
	}, Reviewers: map[string]watch.Reviewer{}}
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
	rows, err := e.svc.PRs.RoomMergeStatus(alpha.Name)
	if err != nil || len(rows) != 1 || rows[0].ChangeID != "a" || rows[0].State != "review" {
		t.Fatalf("alpha rows: %+v %v", rows, err)
	}
	if _, err := e.svc.PRs.RoomMergeStatus("no-such-room"); err == nil {
		t.Fatal("unknown room answered")
	}
}

func TestMergeParallelSettingsKeepOtherRoomPreferences(t *testing.T) {
	e := newEnv(t)
	r := e.room("merge-limit")
	cfg, err := e.svc.PRs.PRSettings(r.ID)
	if err != nil || cfg.MergeMaxParallel != 3 {
		t.Fatalf("defaults=%+v error=%v", cfg, err)
	}
	cfg.MergeMaxParallel = 2
	if _, err := e.svc.PRs.SetPRSettings(ownerCtx, r.ID, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.PRs.SetPRSettings(ownerCtx, r.ID, prview.PRConfig{ReviewPublishing: watch.PublishingGitHub}); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.PRs.PRSettings(r.ID)
	if err != nil || got.MergeMaxParallel != 2 || got.ReviewPublishing != watch.PublishingGitHub {
		t.Fatalf("saved=%+v error=%v", got, err)
	}
	got.MergeMaxParallel = -1
	if _, err := e.svc.PRs.SetPRSettings(ownerCtx, r.ID, got); err == nil {
		t.Fatal("accepted negative limit")
	}
}
