package roomsvc

import (
	prview "dyna-agent/internal/pr/view"
	prfixture "dyna-agent/internal/pr/view/testfixture"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/room"
	roomfixture "dyna-agent/internal/room/testfixture"
)

func TestPRSettingsRowsActionsAndCloseGuard(t *testing.T) {
	e := newEnv(t)
	// Untracking refreshes GitHub state. Keep this PR open so resume and the
	// close guard remain part of the same lifecycle without any host gh call.
	ghLog := prfixture.FakeGHWithPull(t, watch.Pull{Number: 17, State: "open"})
	alpha, beta := e.room("alpha"), e.room("beta")
	alpha = roomfixture.Repository(t, e.svc.Notify.Rooms, alpha.ID, "github.com", "owner/repo", "read-write")
	author := e.start(alpha, "author", nil)
	reviewer := e.start(alpha, "reviewer", nil)
	const key = "github.com/owner/repo#17"
	st := watch.State{Version: 1, Records: map[string]*watch.Record{key: {Key: key, Host: "github.com", Repo: "owner/repo", Number: 17, URL: "https://github.com/owner/repo/pull/17", Title: "Fix parser", RoomID: alpha.ID, Author: author.ID, State: "open", Watching: true, CI: "failed", Failures: 3, CIPaused: true, PauseReason: "CI wakes paused", Created: time.Now(), Checks: []watch.CheckSummary{{Name: "unit", State: "failure", Status: "completed", Conclusion: "failure"}}}}, Reviewers: map[string]watch.Reviewer{}}
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
	cfg := prview.PRConfig{ReviewerProfile: "mock"}
	got, err := e.svc.PRs.SetPRSettings(ownerCtx, alpha.ID, cfg)
	if err != nil || got.ReviewerProfile != "mock" {
		t.Fatalf("set settings: %+v %v", got, err)
	}
	if read, err := e.svc.PRs.PRSettings(alpha.ID); err != nil || !reflect.DeepEqual(read, got) {
		t.Fatalf("read settings: %+v %v", read, err)
	}
	rows, err := e.svc.PRs.Changes(alpha.ID, "", 0)
	if err != nil || len(rows.Items) != 1 || rows.Items[0].Status != "paused" || rows.Items[0].Title != "Fix parser" || len(rows.Items[0].Checks) != 1 || rows.Items[0].Checks[0].State != "failure" {
		t.Fatalf("PR rows: %+v %v", rows, err)
	}
	if rows, err = e.svc.PRs.Changes(beta.ID, "", 0); err != nil || len(rows.Items) != 0 {
		t.Fatalf("foreign room rows: %+v %v", rows, err)
	}
	if rows, err = e.svc.PRs.Changes("", "", 0); err != nil || len(rows.Items) != 1 {
		t.Fatalf("all room rows: %+v %v", rows, err)
	}
	if _, err = e.svc.CloseSession(e.ctx, alpha.ID, author.ID); err == nil || !strings.HasPrefix(err.Error(), "responsibilities:") {
		t.Fatalf("app author close guard: %v", err)
	}
	if _, err = e.svc.PRs.SetPRSettings(ownerCtx, alpha.ID, prview.PRConfig{ReviewerProfile: "missing"}); err == nil {
		t.Fatal("accepted missing reviewer profile")
	}
	if _, err = e.svc.PRs.SetPRSettings(ownerCtx, alpha.ID, prview.PRConfig{ReviewerProfile: "mock", ReviewerSession: reviewer.ID}); err == nil {
		t.Fatal("accepted two reviewer choices")
	}
	if _, err = e.svc.PRs.SetPRSettings(ownerCtx, alpha.ID, prview.PRConfig{ReviewerProfile: "mock"}); err != nil {
		t.Fatal(err)
	}
	if err = e.svc.PRs.UntrackPR(ownerCall, beta.ID, key); err == nil {
		t.Fatal("untracked a PR in another room")
	}
	if err = e.svc.PRs.ResumePR(ownerCall, beta.ID, key); err == nil {
		t.Fatal("resumed a PR in another room")
	}
	if err = e.svc.PRs.UntrackPR(ownerCall, alpha.ID, key); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(ghLog)
	if err != nil || !strings.Contains(string(calls), "repos/owner/repo/pulls/17") {
		t.Fatalf("untrack did not refresh fixture PR: %s %v", calls, err)
	}
	if rows, _ = e.svc.PRs.Changes(alpha.ID, "", 0); rows.Items[0].Status != "untracked" {
		t.Fatalf("untrack status: %+v", rows)
	}
	if err = e.svc.PRs.ResumePR(ownerCall, alpha.ID, key); err != nil {
		t.Fatal(err)
	}
	if rows, _ = e.svc.PRs.Changes(alpha.ID, "", 0); rows.Items[0].CIPaused || rows.Items[0].ConsecutiveFailures != 0 || !rows.Items[0].Watching {
		t.Fatalf("resume state: %+v", rows)
	}
	if closed, err := e.svc.ForceCloseSession(e.ctx, alpha.ID, author.ID); err != nil || !closed.Closed {
		t.Fatalf("app force close: %+v %v", closed, err)
	}
	if cfg, err := e.svc.PRs.SetPRSettings(ownerCtx, alpha.ID, prview.PRConfig{}); err != nil || cfg.ReviewerProfile != "" {
		t.Fatalf("empty settings did not clear reviewer profile: %+v: %v", cfg, err)
	}
}

func TestPREmptyRead(t *testing.T) {
	e := newEnv(t)
	root := e.svc.PRs.Watcher().Store.Root
	if rows, err := e.svc.PRs.Changes("", "", 0); err != nil || len(rows.Items) != 0 {
		t.Fatalf("empty PRs: %+v %v", rows, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("read created an unused PR store")
	}
}

func TestPRWatchDetectsExternalStateAndSettingChanges(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	signatures, version := map[string]string{}, ""
	work := map[string]string{}
	var events []Event
	emit := func(e Event) { events = append(events, e) }
	e.svc.scanPRs(signatures, work, &version, true, emit)
	st := watch.State{Version: 1, Records: map[string]*watch.Record{}, Reviewers: map[string]watch.Reviewer{rec.ID: {Profile: "mock"}}}
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
	e.svc.scanPRs(signatures, work, &version, false, emit)
	e.svc.scanPRs(signatures, work, &version, false, emit)
	if len(events) != 1 || events[0].Kind != "prs" || events[0].RoomID != rec.ID {
		t.Fatalf("settings event: %+v", events)
	}
	st.Records["pr"] = &watch.Record{Key: "pr", RoomID: rec.ID, State: "open", Watching: true, CI: "pending"}
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
	e.svc.scanPRs(signatures, work, &version, false, emit)
	if len(events) != 3 || events[1].Kind != "changes" || events[2].Kind != "prs" || events[1].Change == nil || events[2].Change == nil {
		t.Fatalf("external watcher record event: %+v", events)
	}
}

func TestPRFeedExposesIntegrationKind(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	result, err := e.svc.Notify.Post(e.ctx, notify.PostInput{Event: room.PREvent{Type: room.PRCIFailed, Label: "PR #1", Location: "github.com/o/r#1", ChangeID: "github.com/o/r#1", Number: 1, CIAttempt: 1, CIMax: 3, Summary: "unit", LogTail: "Log tail:\nexample"},
		Sender: notify.Sender{Kind: notify.SenderOwner, Room: rec.Name}, Mentions: []string{"@" + rec.Name}})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := e.svc.FeedTail(rec.ID, "", 0)
	if err != nil || len(feed.Posts) != 1 || feed.Posts[0].ID != result.Message.ID || feed.Posts[0].Kind != "pr" {
		t.Fatalf("PR feed kind: %+v %v", feed, err)
	}
	b, _ := json.Marshal(feed.Posts[0])
	if !strings.Contains(string(b), `"kind":"pr"`) {
		t.Fatalf("PR kind missing from transport: %s", b)
	}
}

func TestLocalChangeAndPlanUpdatesEmitTheirEvents(t *testing.T) {
	e := newEnv(t)
	rec := e.room("local-events")
	st := watch.State{Version: 1, Records: map[string]*watch.Record{"local": {Key: "local", ChangeID: "change_local", Kind: "local", RoomID: rec.ID, Head: "old", State: "open", Watching: true}}, Reviewers: map[string]watch.Reviewer{}}
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
	signatures := map[string]string{}
	work := map[string]string{}
	version := ""
	events := []Event{}
	emit := func(event Event) { events = append(events, event) }
	e.svc.scanPRs(signatures, work, &version, true, emit)
	st.Records["local"].Notes.Intent = "New local author intent"
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
	e.svc.scanPRs(signatures, work, &version, false, emit)
	if len(events) != 1 || events[0].Kind != "changes" || events[0].RoomID != rec.ID {
		t.Fatalf("local refresh events: %+v", events)
	}
	st.Reviewers[rec.ID] = watch.Reviewer{Profile: "sol"}
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
	e.svc.scanPRs(signatures, work, &version, false, emit)
	if len(events) != 2 {
		t.Fatalf("reviewer settings change did not emit: %+v", events)
	}
}
