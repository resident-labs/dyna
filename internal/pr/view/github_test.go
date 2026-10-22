package view_test

import (
	"context"
	"dyna-agent/internal/authority"
	"os"
	"os/exec"
	"strings"
	"testing"

	"dyna-agent/internal/ghclient"
	prview "dyna-agent/internal/pr/view"
	prfixture "dyna-agent/internal/pr/view/testfixture"
	"dyna-agent/internal/pr/watch"
)

func TestGitHubListViewAndDiff(t *testing.T) {
	e, rec, root := workspaceEnv(t)
	log := prfixture.FakeGH(t)
	t.Setenv("GIT_DIR", "/elsewhere")

	prs, err := e.svc.PRs.GitHubList(rec.ID, "pr", "", "author:@me")
	if err != nil {
		t.Fatal(err)
	}
	if prs.Repo != "acme/widgets" || prs.State != "open" || prs.More || len(prs.Items) != 1 {
		t.Fatalf("pr list: %+v", prs)
	}
	pr := prs.Items[0]
	if pr.Number != 7 || !pr.IsDraft || pr.HeadRefName != "feat/knobs" || pr.Additions != 12 || len(pr.Labels) != 1 || pr.Author.Login != "sam" {
		t.Fatalf("pr row: %+v", pr)
	}

	issues, err := e.svc.PRs.GitHubList(rec.ID, "issue", "all", "")
	if err != nil {
		t.Fatal(err)
	}
	if it := issues.Items[0]; it.Number != 3 || it.StateReason != "COMPLETED" || !it.Author.IsBot || it.Labels == nil || len(it.LinkedPRs) != 1 || !it.LinkedPRs[0].IsDraft || it.LinkedPRs[0].State != "OPEN" {
		t.Fatalf("issue row: %+v", it)
	}

	d, err := e.svc.PRs.GitHubView(rec.ID, "pr", 7)
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != "pr" || d.Body != "Adds **knobs**." || len(d.Comments) != 1 || len(d.Reviews) != 1 || d.Reviews[0].State != "APPROVED" || len(d.Files) != 1 || d.Mergeable != "MERGEABLE" {
		t.Fatalf("pr view: %+v", d)
	}
	want := []prview.GitHubCheck{{Name: "go-test", Workflow: "CI", Result: "failure", URL: "d"}, {Name: "lint", Result: "pending"}, {Name: "CodeRabbit", Result: "success"}}
	for i, c := range want {
		if d.Checks[i] != c {
			t.Fatalf("check %d: %+v, want %+v", i, d.Checks[i], c)
		}
	}

	if len(d.Commits) != 2 || d.Commits[0] != (prview.GitHubCommit{SHA: "1111111aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Headline: "Add knobs", Author: prview.GitHubUser{Login: "sam", Name: "Sam"}, Date: d.Commits[0].Date}) || d.Commits[0].Date.IsZero() || d.Commits[1].Headline != "Fix knob range" {
		t.Fatalf("pr commits: %+v", d.Commits)
	}

	issueDetail, err := e.svc.PRs.GitHubView(rec.ID, "issue", 4)
	if err != nil || issueDetail.Group != "has-pr" || len(issueDetail.LinkedPRs) != 1 || issueDetail.LinkedPRs[0].Number != 8 || issueDetail.LinkedPRs[0].IsDraft || issueDetail.LinkedPRs[0].State != "OPEN" || len(issueDetail.Comments) != 1 || !issueDetail.Comments[0].Author.IsBot {
		t.Fatalf("issue detail: %+v, %v", issueDetail, err)
	}

	diff, err := e.svc.PRs.GitHubDiff(rec.ID, 7)
	if err != nil || !strings.Contains(diff.Text, "+knob") || diff.Truncated {
		t.Fatalf("diff: %+v %v", diff, err)
	}

	// gh's own message reaches the owner.
	if _, err := e.svc.PRs.GitHubView(rec.ID, "issue", 99); err == nil || !strings.Contains(err.Error(), "Could not resolve") {
		t.Fatalf("issue view error: %v", err)
	}

	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(b)
	for _, s := range []string{
		root + "|pr list --state open --limit 60 --json ",
		"--search author:@me",
		root + "|api graphql ",
		"closedByPullRequestsReferences(first:100, includeClosedPrs:true)",
		"number state isDraft url",
		"pr diff 7 --color never",
	} {
		if !strings.Contains(calls, s) {
			t.Fatalf("gh calls lack %q:\n%s", s, calls)
		}
	}
}

func TestGitHubListRejectsBadInput(t *testing.T) {
	e, rec, _ := workspaceEnv(t)
	prfixture.FakeGH(t)
	for _, tc := range [][2]string{{"pull", "open"}, {"issue", "merged"}, {"pr", "draft"}} {
		if _, err := e.svc.PRs.GitHubList(rec.ID, tc[0], tc[1], ""); err == nil {
			t.Fatalf("GitHubList(%q, %q) should fail", tc[0], tc[1])
		}
	}
	if _, err := e.svc.PRs.GitHubView(rec.ID, "pr", 0); err == nil {
		t.Fatal("number 0 should fail")
	}
}

func TestGitHubMissingCLI(t *testing.T) {
	e, rec, _ := workspaceEnv(t)
	t.Setenv(ghclient.BinaryEnv, "")
	t.Setenv("PATH", t.TempDir())
	_, err := e.svc.PRs.GitHubList(rec.ID, "pr", "open", "")
	if err == nil || !strings.Contains(err.Error(), "gh auth login") {
		t.Fatalf("missing gh: %v", err)
	}
}

func TestTrackPRUsesRoomSessionAndResumesExistingRecord(t *testing.T) {
	e, rec, _ := workspaceEnv(t)
	otherRoom := e.room("other")
	author := e.start(rec, "author", nil)
	other := e.start(otherRoom, "other", nil)
	prfixture.FakeGH(t)
	// The test must never launch a host watcher.
	e.mgr.Executable = ""
	if _, err := e.svc.PRs.TrackPR(ownerCtx, rec.ID, author.ID, 0); err == nil {
		t.Fatal("accepted PR number 0")
	}
	if _, err := e.svc.PRs.TrackPR(ownerCtx, rec.ID, other.ID, 7); err == nil {
		t.Fatal("accepted a session from another room")
	}
	// A session tracks a PR as itself; naming another session is refused.
	peer := e.start(rec, "peer", nil)
	asAuthor := authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Session: author.ID, Room: rec.ID})
	if _, err := e.svc.PRs.TrackPR(asAuthor, rec.ID, peer.ID, 7); err == nil || !strings.Contains(err.Error(), "tracks a PR only as itself") {
		t.Fatalf("a session tracked a PR for another session: %v", err)
	}
	asWorker := authority.WithActor(context.Background(), authority.Actor{Role: authority.Worker, Run: "run_a", Room: rec.ID})
	if _, err := e.svc.PRs.TrackPR(asWorker, rec.ID, peer.ID, 7); err == nil || !strings.Contains(err.Error(), "tracks a PR only as itself") {
		t.Fatalf("a workflow worker tracked a PR for a session: %v", err)
	}
	key := "github.com/acme/widgets#7"
	st := watch.State{Version: 1, Records: map[string]*watch.Record{
		key: {Key: key, Host: "github.com", Repo: "acme/widgets", Number: 7, RoomID: rec.ID, Author: author.ID, State: "open", Watching: false, CIPaused: true, Failures: 3},
	}}
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
	got, err := e.svc.PRs.TrackPR(ownerCtx, rec.ID, author.ID, 7)
	if err != nil || !got.Watching || got.CIPaused || got.ConsecutiveFailures != 0 || got.AuthorSession != author.ID {
		t.Fatalf("resume tracked PR: %+v %v", got, err)
	}
	st.Records[key].RoomID = otherRoom.ID
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
	if _, err := e.svc.PRs.TrackPR(ownerCtx, rec.ID, author.ID, 7); err == nil || !strings.Contains(err.Error(), "another room") {
		t.Fatalf("accepted PR tracked in another room: %v", err)
	}
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, watch.State{Version: 1, Records: map[string]*watch.Record{}})
	got, err = e.svc.PRs.TrackPR(ownerCtx, rec.ID, author.ID, 7)
	if err != nil || got.State != "merged" || got.Watching || got.AuthorSession != author.ID {
		t.Fatalf("new tracked PR: %+v %v", got, err)
	}
}

func TestIssueQueriesUseRepositoryHost(t *testing.T) {
	e, rec, _ := workspaceEnv(t)
	log := prfixture.FakeGH(t)
	t.Setenv("GH_TEST_HOST", "code.example.com")
	if _, err := e.svc.PRs.GitHubList(rec.ID, "issue", "open", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.PRs.GitHubView(rec.ID, "issue", 3); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(b), "--hostname code.example.com"); got != 2 {
		t.Fatalf("issue queries used the wrong host (%d): %s", got, b)
	}
}

func TestIssueViewPreservesAllCommentPages(t *testing.T) {
	e, rec, _ := workspaceEnv(t)
	log := prfixture.FakeGH(t)
	t.Setenv("GH_TEST_LONG_COMMENTS", "true")
	got, err := e.svc.PRs.GitHubView(rec.ID, "issue", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Comments) != 149 || got.Comments[0].Body != "comment-0" || got.Comments[148].Body != "comment-148" {
		t.Fatalf("lost comment pages: %d comments", len(got.Comments))
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"--paginate", "$endCursor:String", "after:$endCursor", "hasNextPage endCursor"} {
		if !strings.Contains(string(b), part) {
			t.Errorf("query lacks %q", part)
		}
	}
}

func TestIssueQueryStringVariablesKeepTheirTypes(t *testing.T) {
	e, rec, _ := workspaceEnv(t)
	log := prfixture.FakeGH(t)
	t.Setenv("GH_TEST_OWNER", "true")
	t.Setenv("GH_TEST_REPO", "123")
	if _, err := e.svc.PRs.GitHubList(rec.ID, "issue", "open", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.PRs.GitHubView(rec.ID, "issue", 3); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"-f owner=true", "-f repo=123"} {
		if strings.Count(string(b), part) != 2 {
			t.Errorf("queries lack string field %q: %s", part, b)
		}
	}
	if !strings.Contains(string(b), "-f search=repo:true/123") || !strings.Contains(string(b), "-F number=3") {
		t.Errorf("wrong search or number types: %s", b)
	}
}

func TestGitHubRoomAccessSetting(t *testing.T) {
	e, rec, _ := workspaceEnv(t)
	cfg, err := e.svc.PRs.SetPRSettings(ownerCtx, rec.ID, prview.PRConfig{GitHubAccess: "read-only"})
	if err != nil || cfg.GitHubAccess != "read-only" {
		t.Fatalf("set access: %+v %v", cfg, err)
	}
	cfg, err = e.svc.PRs.SetPRSettings(ownerCtx, rec.ID, prview.PRConfig{})
	if err != nil || cfg.GitHubAccess != "read-only" {
		t.Fatalf("omitted access changed policy: %+v %v", cfg, err)
	}
	if _, err = e.svc.PRs.SetPRSettings(ownerCtx, rec.ID, prview.PRConfig{GitHubAccess: "invalid"}); err == nil {
		t.Fatal("accepted invalid access")
	}
	cfg, err = e.svc.PRs.SetPRSettings(ownerCtx, rec.ID, prview.PRConfig{GitHubAccess: "read-write"})
	if err != nil || cfg.GitHubAccess != "read-write" {
		t.Fatalf("enable writes: %+v %v", cfg, err)
	}
	cfg, err = e.svc.PRs.PRSettings(rec.ID)
	if err != nil || cfg.GitHubAccess != "read-write" {
		t.Fatalf("read access: %+v %v", cfg, err)
	}
}

// The CLI's track names a checkout path, which the engine does not trust:
// it must be a checkout of the room's repository and of the PR's. A session
// tracks only as itself.
func TestTrackPRRefChecksTheCheckoutAndTheCaller(t *testing.T) {
	e, rec, root := workspaceEnv(t)
	author := e.start(rec, "author", nil)
	peer := e.start(rec, "peer", nil)
	prfixture.FakeGH(t)
	e.mgr.Executable = ""
	gitRepo := func(dir, remote string) {
		t.Helper()
		for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"remote", "add", "origin", remote}, {"-c", "user.name=t", "-c", "user.email=t@e", "commit", "-q", "--allow-empty", "-m", "init"}} {
			if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v %s", args, err, out)
			}
		}
	}
	// The room fixture already made root a checkout with an acme/widgets origin.
	foreign := t.TempDir()
	gitRepo(foreign, "https://github.com/other/repo.git")
	if _, err := e.svc.PRs.TrackPRRef(ownerCtx, rec.ID, author.ID, "7", foreign, ""); err == nil || !strings.Contains(err.Error(), "not a checkout of room") {
		t.Fatalf("a checkout of another repository was accepted: %v", err)
	}
	asAuthor := authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Session: author.ID, Room: rec.ID})
	// Only the owner or root names another author: a session, even the
	// author's parent, and a workflow worker track only as themselves.
	child := e.start(rec, "child", &author)
	for who, ctx := range map[string]context.Context{
		"a peer session":       asAuthor,
		"the author's parent":  asAuthor,
		"a worker of the room": authority.WithActor(context.Background(), authority.Actor{Role: authority.Worker, Run: "run_a", Room: rec.ID}),
	} {
		named := peer.ID
		if who == "the author's parent" {
			named = child.ID
		}
		if _, err := e.svc.PRs.TrackPRRef(ctx, rec.ID, named, "7", "", ""); err == nil || !strings.Contains(err.Error(), "tracks a PR only as itself") {
			t.Fatalf("%s tracked a PR for another session: %v", who, err)
		}
	}
	other := e.room("track-other")
	asOtherRoot := authority.WithActor(context.Background(), authority.Actor{Role: authority.Root, Session: "ses_20261020-000000-0000abcd", Room: other.ID})
	if _, err := e.svc.PRs.TrackPRRef(asOtherRoot, rec.ID, author.ID, "7", "", ""); err == nil || !strings.Contains(err.Error(), "only in its own room") {
		t.Fatalf("root of another room tracked a PR here: %v", err)
	}
	got, err := e.svc.PRs.TrackPRRef(asAuthor, rec.ID, author.ID, "7", root, "")
	if err != nil || got.PR.Number != 7 || got.PR.Author != author.ID || !got.Created {
		t.Fatalf("track from the room's checkout = %+v, %v", got, err)
	}
}

// Root names no session when it tracks, because the CLI sends its own empty
// author and the engine already authenticated root. Root acts as its session.
func TestTrackPRRefRootTracksAsItsOwnSession(t *testing.T) {
	e, rec, root := workspaceEnv(t)
	prfixture.FakeGH(t)
	seat := e.start(rec, "root-seat", nil)
	asRoot := authority.WithActor(context.Background(), authority.Actor{Role: authority.Root, Session: seat.ID, Room: rec.ID})
	got, err := e.svc.PRs.TrackPRRef(asRoot, rec.ID, "", "7", root, "")
	if err != nil || got.PR.Author != seat.ID || !got.Created {
		t.Fatalf("root track = %+v, %v", got, err)
	}
}
