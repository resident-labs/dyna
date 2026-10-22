package view_test

import (
	"strings"
	"testing"

	"dyna-agent/internal/ghclient"
	prfixture "dyna-agent/internal/pr/view/testfixture"
	"dyna-agent/internal/pr/watch"
)

func TestPRNoiseRulesScopeAndRemoval(t *testing.T) {
	e, rec, _ := workspaceEnv(t)
	prfixture.FakeGH(t) // current repository: github.com/acme/widgets
	shared := e.room("shared")
	foreign := e.room("foreign")
	st := watch.State{Version: 1, Records: map[string]*watch.Record{
		"tracked": {RoomID: rec.ID, Host: "enterprise.test", Repo: "team/tools", State: "merged"},
		"shared":  {RoomID: shared.ID, Host: "GITHUB.COM", Repo: "ACME/WIDGETS"},
		"foreign": {RoomID: foreign.ID, Host: "github.com", Repo: "other/private"},
	}, NoiseRules: map[string]watch.NoiseRule{
		"current": {ID: "current", Host: "GitHub.com", Repo: "Acme/Widgets", Bot: "bot", Template: "review quota reached", Reason: "Quota notice", SourceURL: "https://github.com/acme/widgets/pull/7#issuecomment-1", Hits: 3},
		"tracked": {ID: "tracked", Host: "enterprise.test", Repo: "team/tools"},
		"host":    {ID: "host", Host: "unrelated.test", Repo: "acme/widgets"},
		"foreign": {ID: "foreign", Host: "github.com", Repo: "other/private"},
	}}
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
	list, err := e.svc.PRs.PRNoiseRules(rec.ID)
	if err != nil || len(list.Rules) != 2 || len(list.Repositories) != 2 || list.Warning != "" {
		t.Fatalf("room rules: %+v %v", list, err)
	}
	if rule := list.Rules[0]; rule.ID != "current" || rule.Reason != "Quota notice" || rule.Template == "" || rule.SourceURL == "" || rule.Hits != 3 {
		t.Fatalf("rule metadata: %+v", rule)
	}
	for _, id := range []string{"host", "foreign", "missing"} {
		if err := e.svc.PRs.RemovePRNoiseRule(ownerCtx, rec.ID, id); err == nil {
			t.Fatalf("removed out-of-scope or missing rule %s", id)
		}
	}
	if err := e.svc.PRs.RemovePRNoiseRule(ownerCtx, "missing-room", "current"); err == nil {
		t.Fatal("accepted a missing room")
	}
	// A second room for the same repository shares the rule and may remove it.
	if err := e.svc.PRs.RemovePRNoiseRule(ownerCtx, shared.ID, "current"); err != nil {
		t.Fatal(err)
	}
	list, err = e.svc.PRs.PRNoiseRules(rec.ID)
	if err != nil || len(list.Rules) != 1 || list.Rules[0].ID != "tracked" {
		t.Fatalf("shared removal: %+v %v", list, err)
	}
	if err := e.svc.PRs.RemovePRNoiseRule(ownerCtx, rec.ID, "tracked"); err != nil {
		t.Fatal(err)
	}
	left, err := e.svc.PRs.Watcher().ListNoise()
	if err != nil || len(left) != 2 || left[0].ID != "foreign" || left[1].ID != "host" {
		t.Fatalf("unrelated rules changed: %+v %v", left, err)
	}
}

func TestPRNoiseRulesMissingRepositoryAndLookupFailure(t *testing.T) {
	e, rec, _ := workspaceEnv(t)
	empty := e.room("empty")
	if list, err := e.svc.PRs.PRNoiseRules(empty.ID); err != nil || list.Rules == nil || list.Repositories == nil || len(list.Rules) != 0 {
		t.Fatalf("empty room: %+v %v", list, err)
	}
	if err := e.svc.PRs.RemovePRNoiseRule(ownerCtx, empty.ID, "rule"); err == nil {
		t.Fatal("empty scope permitted removal")
	}
	t.Setenv(ghclient.BinaryEnv, "")
	t.Setenv("PATH", t.TempDir())
	if _, err := e.svc.PRs.PRNoiseRules(rec.ID); err == nil || !strings.Contains(err.Error(), "gh auth login") {
		t.Fatalf("repository lookup failure: %v", err)
	}
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, watch.State{Version: 1, Records: map[string]*watch.Record{
		"tracked": {RoomID: rec.ID, Host: "github.com", Repo: "owner/repo"},
	}, NoiseRules: map[string]watch.NoiseRule{
		"rule": {ID: "rule", Host: "github.com", Repo: "owner/repo"},
	}})
	if list, err := e.svc.PRs.PRNoiseRules(rec.ID); err != nil || len(list.Rules) != 1 || list.Warning == "" {
		t.Fatalf("tracked fallback: %+v %v", list, err)
	}
	if err := e.svc.PRs.RemovePRNoiseRule(ownerCtx, rec.ID, "rule"); err != nil {
		t.Fatalf("tracked fallback removal: %v", err)
	}
}

func TestPRFilteredCommentCountUsesLatestDurableDecisions(t *testing.T) {
	e := newEnv(t)
	rec := e.room("counts")
	r := &watch.Record{Key: "pr", RoomID: rec.ID, State: "open", CommentDecisions: map[string]watch.Decision{
		"issue-comments:1": {Action: "drop", Reason: "built-in noise"},
		"reviews:2":        {Action: "drop", Reason: "learned_noise:removed-rule"},
		"comments:3":       {Action: "keep", Reason: "finding"},
	}, PendingDrops: map[string]int{"pending": 99}, Notices: []watch.Notice{{Done: false}}}
	st := watch.State{Version: 1, Records: map[string]*watch.Record{
		"pr": r, "another": {Key: "another", RoomID: rec.ID, State: "open"},
	}}
	check := func(want int) {
		t.Helper()
		prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
		rows, err := e.svc.PRs.Changes(rec.ID, "", 0)
		if err != nil || len(rows.Items) != 2 || rows.Items[0].FilteredComments != 0 || rows.Items[1].FilteredComments != want {
			t.Fatalf("count want %d: %+v %v", want, rows, err)
		}
	}
	check(2)
	r.PendingDrops = nil
	r.Notices[0].Done = true
	check(2) // delivery clears pending counts, not saved decisions
	r.CommentDecisions["reviews:2"] = watch.Decision{Action: "keep", Reason: "edited to add a finding"}
	check(1) // an edited comment replaces its earlier decision
	r.CommentDecisions["comments:3"] = watch.Decision{Action: "drop"}
	check(2)
}
