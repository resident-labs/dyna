package engine

import (
	"slices"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// githubWrites is the owner's to change, is checked when it is set, and is what the room's boxes get and what dyna_room says.
func TestRoomGitHubWritesSetting(t *testing.T) {
	r := newGrantRig(t)
	ctx := t.Context()
	for _, repo := range []contract.RepoName{"acme/app", "acme/lib"} {
		if err := r.s.store.AddRoomRepo(ctx, r.room, repo, repo == "acme/app"); err != nil {
			t.Fatal(err)
		}
	}
	set := func(who contract.Principal, writes contract.GitHubWrites) *contract.Error {
		_, problem := r.s.RoomSet(ctx, who, contract.RoomSetRequest{Room: r.room, GitHubWrites: &writes})
		return problem
	}
	groups := func(g ...contract.GitHubWriteGroup) []contract.GitHubWriteGroup { return g }
	reasonOf := func(problem *contract.Error) contract.GitHubWriteReason {
		if problem == nil || problem.GitHubWrites == nil {
			return ""
		}
		return problem.GitHubWrites.Reason
	}
	issues := contract.GitHubWrites{Repos: []contract.RepoName{"acme/app"}, Groups: groups(contract.GitHubWriteIssues)}

	// Only the owner changes it, the root and a worker do not, and the refusal says so by cause.
	for _, who := range []contract.Principal{r.root, r.worker} {
		if problem := set(who, issues); problem == nil || problem.Code != contract.ErrForbidden || reasonOf(problem) != contract.GitHubWriteOwnerOnly {
			t.Fatalf("a session set it: %+v", problem)
		}
	}
	if got, _ := r.s.GitHubWritesOf(ctx, r.room); got.On() {
		t.Fatalf("on by default or after refusals: %+v", got)
	}

	// Checked at the boundary: a repository outside the room, a group that does not exist, groups and no repositories.
	if problem := set(r.owner, contract.GitHubWrites{Repos: []contract.RepoName{"acme/secret"}, Groups: groups(contract.GitHubWriteIssues)}); reasonOf(problem) != contract.GitHubWriteRepoNotInRoom {
		t.Fatalf("a repo outside the room: %+v", problem)
	}
	if problem := set(r.owner, contract.GitHubWrites{Repos: []contract.RepoName{"acme/app", "acme/secret"}, Groups: groups(contract.GitHubWriteIssues)}); reasonOf(problem) != contract.GitHubWriteRepoNotInRoom {
		t.Fatalf("one repo outside the room: %+v", problem)
	}
	if problem := set(r.owner, contract.GitHubWrites{Repos: []contract.RepoName{"acme/app"}, Groups: groups("merge")}); reasonOf(problem) != contract.GitHubWriteUnknownGroup {
		t.Fatalf("an unknown group: %+v", problem)
	}
	if problem := set(r.owner, contract.GitHubWrites{Groups: groups(contract.GitHubWriteIssues)}); problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatalf("groups without repos: %+v", problem)
	}
	if got, _ := r.s.GitHubWritesOf(ctx, r.room); got.On() {
		t.Fatalf("a refused value was stored: %+v", got)
	}

	// Set, it reaches the room's boxes and dyna_room; another room has none.
	if problem := set(r.owner, contract.GitHubWrites{Repos: []contract.RepoName{"ACME/App"}, Groups: groups(contract.GitHubWriteProjects, contract.GitHubWriteIssues)}); problem != nil {
		t.Fatal(problem)
	}
	got, _ := r.s.GitHubWritesOf(ctx, r.room)
	if !slices.Equal(got.Groups, groups(contract.GitHubWriteIssues, contract.GitHubWriteProjects)) || !slices.Equal(got.Repos, []contract.RepoName{"acme/app"}) {
		t.Fatalf("stored: %+v", got)
	}
	if other, err := r.s.GitHubWritesOf(ctx, "room_other"); err == nil && other.On() {
		t.Fatalf("another room got %+v", other)
	}
	section := func(who contract.Principal) string {
		res, problem := r.s.ContextSnapshot(ctx, who, contract.ContextSnapshotRequest{})
		if problem != nil {
			t.Fatal(problem)
		}
		for _, sec := range res.Sections {
			if sec.Key == contract.ContextRoom {
				return sec.Text
			}
		}
		return ""
	}
	for _, who := range []contract.Principal{r.root, r.worker} {
		if text := section(who); text != "GitHub writes: issues, projects on acme/app" {
			t.Fatalf("dyna_room: %q", text)
		}
	}

	// `*` is the room's repositories and follows the room's list, never another room's or an outside repo.
	if problem := set(r.owner, contract.GitHubWrites{Repos: []contract.RepoName{"acme/app", "*"}, Groups: groups(contract.GitHubWritePulls)}); problem != nil {
		t.Fatal(problem)
	}
	got, _ = r.s.GitHubWritesOf(ctx, r.room)
	if !slices.Equal(got.Repos, []contract.RepoName{"acme/app", "acme/lib"}) || got.Allows("acme/secret") {
		t.Fatalf("*: %+v", got)
	}

	// Off again, and the setting changes nothing about root approvals set in the same request.
	if problem := set(r.owner, contract.GitHubWrites{}); problem != nil {
		t.Fatal(problem)
	}
	if got, _ := r.s.GitHubWritesOf(ctx, r.room); got.On() || section(r.worker) != "" {
		t.Fatalf("off: %+v %q", got, section(r.worker))
	}
}
