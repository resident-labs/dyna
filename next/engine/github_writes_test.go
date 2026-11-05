package engine

import (
	"database/sql"
	"slices"
	"strings"
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

// `*` is the room's repositories as they are when asked: one added later is in it, a repository the room no longer lists is not,
// and neither GitHubWritesOf nor dyna_room ever shows `*` itself. A repository spelled twice is kept once, as the room spells it.
func TestGitHubWritesAllReposFollowsTheRoom(t *testing.T) {
	r := newGrantRig(t)
	ctx := t.Context()
	dropRepo := func(repo string) error {
		return r.s.store.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "DELETE FROM room_repos WHERE room=? AND repo=?", r.room, repo)
			return err
		})
	}
	add := func(repo contract.RepoName) {
		t.Helper()
		if err := r.s.store.AddRoomRepo(ctx, r.room, repo, false); err != nil {
			t.Fatal(err)
		}
	}
	set := func(repos ...contract.RepoName) {
		t.Helper()
		writes := contract.GitHubWrites{Repos: repos, Groups: []contract.GitHubWriteGroup{contract.GitHubWriteIssues}}
		if _, problem := r.s.RoomSet(ctx, r.owner, contract.RoomSetRequest{Room: r.room, GitHubWrites: &writes}); problem != nil {
			t.Fatal(problem)
		}
	}
	granted := func() []contract.RepoName {
		t.Helper()
		got, err := r.s.GitHubWritesOf(ctx, r.room)
		if err != nil {
			t.Fatal(err)
		}
		return got.Repos
	}
	line := func() string {
		res, problem := r.s.ContextSnapshot(ctx, r.worker, contract.ContextSnapshotRequest{})
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
	add("acme/app")
	set("*")
	if got := granted(); !slices.Equal(got, []contract.RepoName{"acme/app"}) || line() != "GitHub writes: issues on acme/app" {
		t.Fatalf("* with one repo: %v %q", got, line())
	}
	add("acme/lib")
	if got := granted(); !slices.Equal(got, []contract.RepoName{"acme/app", "acme/lib"}) || line() != "GitHub writes: issues on acme/app, acme/lib" {
		t.Fatalf("* after a repo is added: %v %q", got, line())
	}
	if err := dropRepo("acme/app"); err != nil {
		t.Fatal(err)
	}
	if got := granted(); !slices.Equal(got, []contract.RepoName{"acme/lib"}) || line() != "GitHub writes: issues on acme/lib" {
		t.Fatalf("* after a repo is removed: %v %q", got, line())
	}
	// A named repository the room stops listing is dropped from what is granted, and with none left nothing is on.
	add("acme/app")
	set("acme/app", "ACME/APP", "Acme/App")
	if got := granted(); !slices.Equal(got, []contract.RepoName{"acme/app"}) {
		t.Fatalf("a repo spelled three ways: %v", got)
	}
	if err := dropRepo("acme/app"); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.s.GitHubWritesOf(ctx, r.room); got.On() || line() != "" {
		t.Fatalf("a named repo the room dropped: %+v %q", got, line())
	}
}

// Groups with no repository name nothing, so room.set refuses them as invalid rather than storing a setting that is on and allows nothing.
func TestGitHubWritesGroupsWithoutReposAreInvalid(t *testing.T) {
	r := newGrantRig(t)
	writes := contract.GitHubWrites{Groups: []contract.GitHubWriteGroup{contract.GitHubWriteIssues}}
	if _, problem := r.s.RoomSet(t.Context(), r.owner, contract.RoomSetRequest{Room: r.room, GitHubWrites: &writes}); problem == nil || !strings.Contains(problem.Message, "no repository") {
		t.Fatalf("problem: %+v", problem)
	}
	if got, _ := r.s.GitHubWritesOf(t.Context(), r.room); got.On() {
		t.Fatalf("stored %+v", got)
	}
}
