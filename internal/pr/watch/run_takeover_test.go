package watch

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/runstore/runstoretest"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

// runTakeoverEnv seeds the session and run records a D-252 takeover needs: a
// controller session, the room's root session, and run-owned workers.
type runTakeoverEnv struct {
	e          *testEnv
	sessions   string
	controller string
	other      string
	root       subagent.Info
}

func newRunTakeoverEnv(t *testing.T) *runTakeoverEnv {
	t.Helper()
	e := newEnv(t)
	sessions := filepath.Join(filepath.Dir(e.s.Store.Root), "sessions")
	env := &runTakeoverEnv{e: e, sessions: sessions,
		controller: "ses_20261017-000005-eeeeeeee",
		other:      "ses_20261017-000006-fabcdefa",
		root:       subagent.Info{Meta: subagent.Meta{ID: "ses_20261017-000007-99999999", Role: subagent.RoleRoot, RoomID: e.author.RoomID, Room: e.author.Room}},
	}
	for _, id := range []string{env.controller, env.other} {
		recordtest.Write(t, sessions, id, subagent.Meta{ID: id, RoomID: e.author.RoomID, Room: e.author.Room})
	}
	recordtest.Write(t, sessions, env.root.ID, env.root.Meta)
	return env
}

// worker starts a run-owned worker session of run, whose run record names
// parent as the session that started the run.
func (env *runTakeoverEnv) worker(t *testing.T, run, parent, id string) subagent.Info {
	t.Helper()
	runstoretest.Write(t, filepath.Dir(env.e.s.Store.Root), runstore.Meta{ID: run, ParentSession: parent, StartedAt: time.Unix(0, 0)})
	info := subagent.Info{Meta: subagent.Meta{ID: id, RoomID: env.e.author.RoomID, Room: env.e.author.Room, Run: &subagent.RunOwner{ID: run}}}
	recordtest.Write(t, env.sessions, id, info.Meta)
	return info
}

// workerBranch gives each test its own branch, so records never collide.
func (env *runTakeoverEnv) workerBranch(t *testing.T, branch string) string {
	t.Helper()
	dir, _ := changeRepo(t)
	changeGit(t, dir, "checkout", "-b", branch)
	changeCommit(t, dir, branch+"\n")
	changeGit(t, dir, "remote", "add", "origin", "https://github.com/owner/repo")
	return dir
}

// closeAuthor closes a run worker and hands its change to the room's root
// session, as the normal session close does.
func (env *runTakeoverEnv) closeAuthor(t *testing.T, from subagent.Info) {
	t.Helper()
	sessions := subagent.Store{Root: env.sessions}
	meta, err := sessions.ReadMeta(from.ID)
	if err != nil {
		t.Fatal(err)
	}
	meta.Closed = time.Unix(0, 0)
	recordtest.Write(t, env.sessions, from.ID, meta)
	if _, err := env.e.s.CloseSession(context.Background(), from, env.root); err != nil {
		t.Fatal(err)
	}
}

func (env *runTakeoverEnv) open(t *testing.T, author subagent.Info, dir string) (Record, bool, error) {
	t.Helper()
	author.Dir = dir
	return env.e.s.OpenChange(context.Background(), authority.Actor{Role: authority.Owner}, author, OpenChangeOptions{Dir: dir, Base: "main", Notes: ChangeNotes{Intent: "D-252"}})
}

func refuseTakeover(t *testing.T, label string, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "this branch belongs to another room or author") {
		t.Fatalf("%s was not refused: %v", label, err)
	}
}

// TestRunAgentTakesOverChangeOfClosedEarlierAuthor is the D-252 case: a later
// run agent of the same controller opens the branch whose earlier run author
// closed and whose role moved to the room's root session.
func TestRunAgentTakesOverChangeOfClosedEarlierAuthor(t *testing.T) {
	env := newRunTakeoverEnv(t)
	dir := env.workerBranch(t, "feature")
	first := env.worker(t, "wf_first", env.controller, "ses_20261017-000008-11111111")
	opened, created, err := env.open(t, first, dir)
	if err != nil || !created {
		t.Fatalf("first open: %+v created=%t %v", opened, created, err)
	}
	if want := state.Controller(env.controller); opened.Controller != want {
		t.Fatalf("open recorded controller %q, want %q", opened.Controller, want)
	}
	env.closeAuthor(t, first)

	second := env.worker(t, "wf_second", env.controller, "ses_20261017-000009-22222222")
	head := changeCommit(t, dir, "fixed\n")
	taken, created, err := env.open(t, second, dir)
	if err != nil || created {
		t.Fatalf("takeover open: %+v created=%t %v", taken, created, err)
	}
	if taken.Author != second.ID || taken.Head != head || taken.ChangeID != opened.ChangeID {
		t.Fatalf("taken-over record: %+v", taken)
	}
	if len(taken.FormerAuthors) != 2 || taken.FormerAuthors[0] != first.ID || taken.FormerAuthors[1] != env.root.ID {
		t.Fatalf("author history after takeover: %+v", taken.FormerAuthors)
	}
	if taken.Controller != state.Controller(env.controller) {
		t.Fatalf("takeover kept controller %q", taken.Controller)
	}
}

// TestRunTakeoverAppliesToRootStartedRuns covers runs root or the owner
// started: their fixed controller is ControllerRoot.
func TestRunTakeoverAppliesToRootStartedRuns(t *testing.T) {
	env := newRunTakeoverEnv(t)
	dir := env.workerBranch(t, "rooted")
	first := env.worker(t, "wf_first", "", "ses_20261017-000008-11111111")
	opened, _, err := env.open(t, first, dir)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if opened.Controller != state.ControllerRoot {
		t.Fatalf("root-started run recorded controller %q", opened.Controller)
	}
	env.closeAuthor(t, first)

	second := env.worker(t, "wf_second", "", "ses_20261017-000009-22222222")
	taken, created, err := env.open(t, second, dir)
	if err != nil || created || taken.Author != second.ID {
		t.Fatalf("takeover of a root-started run's change: %+v created=%t %v", taken, created, err)
	}
}

// TestRunTakeoverRefusesDifferentController keeps the refusal for a caller
// whose run another controller started.
func TestRunTakeoverRefusesDifferentController(t *testing.T) {
	env := newRunTakeoverEnv(t)
	dir := env.workerBranch(t, "split")
	first := env.worker(t, "wf_first", env.controller, "ses_20261017-000008-11111111")
	if _, _, err := env.open(t, first, dir); err != nil {
		t.Fatal(err)
	}
	env.closeAuthor(t, first)

	second := env.worker(t, "wf_second", env.other, "ses_20261017-000009-22222222")
	_, _, err := env.open(t, second, dir)
	refuseTakeover(t, "a different controller", err)
}

// TestRunTakeoverRefusesOpenEarlierAuthor keeps the refusal while the earlier
// run author still holds the role.
func TestRunTakeoverRefusesOpenEarlierAuthor(t *testing.T) {
	env := newRunTakeoverEnv(t)
	dir := env.workerBranch(t, "held")
	first := env.worker(t, "wf_first", env.controller, "ses_20261017-000008-11111111")
	if _, _, err := env.open(t, first, dir); err != nil {
		t.Fatal(err)
	}

	second := env.worker(t, "wf_second", env.controller, "ses_20261017-000009-22222222")
	_, _, err := env.open(t, second, dir)
	refuseTakeover(t, "an open earlier author", err)
}

// TestRunTakeoverRefusesRecordWithoutController keeps the refusal for records
// stored before controllers were recorded.
func TestRunTakeoverRefusesRecordWithoutController(t *testing.T) {
	env := newRunTakeoverEnv(t)
	dir := env.workerBranch(t, "legacy")
	if _, _, err := env.open(t, env.e.author, dir); err != nil {
		t.Fatal(err)
	}
	env.closeAuthor(t, env.e.author)

	second := env.worker(t, "wf_second", env.controller, "ses_20261017-000009-22222222")
	_, _, err := env.open(t, second, dir)
	refuseTakeover(t, "a record without a controller", err)
}

// TestRunTakeoverSurvivesRootRefresh covers the D-252 recovery itself: root,
// which no run owns, refreshed the change to a new head after the run author
// closed, and the controller the run recorded must survive that refresh for
// the next worker of the same controller.
func TestRunTakeoverSurvivesRootRefresh(t *testing.T) {
	env := newRunTakeoverEnv(t)
	dir := env.workerBranch(t, "refreshed")
	first := env.worker(t, "wf_first", env.controller, "ses_20261017-000008-11111111")
	if _, _, err := env.open(t, first, dir); err != nil {
		t.Fatal(err)
	}
	env.closeAuthor(t, first)

	head := changeCommit(t, dir, "root fix\n")
	refreshed, created, err := env.open(t, env.root, dir)
	if err != nil || created {
		t.Fatalf("root refresh: %+v created=%t %v", refreshed, created, err)
	}
	if refreshed.Head != head || refreshed.Author != env.root.ID {
		t.Fatalf("root refresh record: %+v", refreshed)
	}
	if refreshed.Controller != state.Controller(env.controller) {
		t.Fatalf("root refresh lost controller %q", refreshed.Controller)
	}

	second := env.worker(t, "wf_second", env.controller, "ses_20261017-000009-22222222")
	taken, created, err := env.open(t, second, dir)
	if err != nil || created || taken.Author != second.ID {
		t.Fatalf("takeover after root refresh: %+v created=%t %v", taken, created, err)
	}
}

// TestRunTakeoverOnTrackedPRTrack covers `dyna git pr track` of a PR the
// earlier run author tracked: the existing-record path takes the author role
// over instead of returning a change the caller cannot act on.
func TestRunTakeoverOnTrackedPRTrack(t *testing.T) {
	env := newRunTakeoverEnv(t)
	first := env.worker(t, "wf_first", env.controller, "ses_20261017-000008-11111111")
	ref := Ref{Host: "github.com", Repo: "owner/repo", Number: 17}
	tracked := Record{Key: ref.Key(), ChangeID: ref.Key(), Kind: "github", Host: ref.Host, Repo: ref.Repo, Number: ref.Number, URL: ref.URL(),
		RoomID: env.e.author.RoomID, Author: first.ID, Controller: state.Controller(env.controller),
		Branch: "feature", Head: strings.Repeat("a", 40), State: "open", Watching: true, Created: time.Unix(0, 0)}
	if err := env.e.s.Store.update(func(st *State) error {
		st.Records[tracked.Key] = &tracked
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	env.closeAuthor(t, first)

	second := env.worker(t, "wf_second", env.controller, "ses_20261017-000009-22222222")
	got, created, err := env.e.s.Track(context.Background(), authority.Actor{Role: authority.Owner}, ref, second, "")
	if err != nil || created {
		t.Fatalf("track of the earlier run's PR: %+v created=%t %v", got, created, err)
	}
	if got.Author != second.ID || len(got.FormerAuthors) != 2 || got.Controller != state.Controller(env.controller) {
		t.Fatalf("tracked PR after takeover: %+v", got)
	}
}

// TestRunTakeoverOnPRTrack covers the same refusal when a later run agent
// tracks the PR for the branch of the change it takes over.
func TestRunTakeoverOnPRTrack(t *testing.T) {
	env := newRunTakeoverEnv(t)
	dir := env.workerBranch(t, "tracked")
	first := env.worker(t, "wf_first", env.controller, "ses_20261017-000008-11111111")
	if _, _, err := env.open(t, first, dir); err != nil {
		t.Fatal(err)
	}
	env.closeAuthor(t, first)

	second := env.worker(t, "wf_second", env.controller, "ses_20261017-000009-22222222")
	env.e.f.Pull.Head.Ref = "tracked"
	env.e.write()
	ref := Ref{Host: "github.com", Repo: "owner/repo", Number: 17}
	got, created, err := env.e.s.Track(context.Background(), authority.Actor{Role: authority.Owner}, ref, second, dir)
	if err != nil || !created {
		t.Fatalf("track after takeover: %+v created=%t %v", got, created, err)
	}
	if got.Author != second.ID || got.Kind != "github" || got.Controller != state.Controller(env.controller) {
		t.Fatalf("attached record: %+v", got)
	}
}
