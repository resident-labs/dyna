package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/lanesync"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/trial"
	"dyna-agent/internal/room"
)

// publishChange puts a change on the integration branch and marks it merged without running a merge trial.
func publishChange(t *testing.T, e *testEnv, repo string, r Record) {
	t.Helper()
	changeGit(t, repo, "update-ref", "refs/heads/dyna/integration", r.Head)
	if err := e.s.Store.update(func(st *State) error {
		endRecord(st.Records[r.Key], true, e.clock)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func syncEnv(t *testing.T) (*testEnv, string, string, *fakeLocalCI) {
	t.Helper()
	e, dir, records, ci := localMergeEnvFor(t, false, "good")
	publishChange(t, e, dir, records[0])
	changeGit(t, dir, "checkout", "main")
	if err := os.WriteFile(filepath.Join(dir, "upstream.txt"), []byte("upstream"), 0600); err != nil {
		t.Fatal(err)
	}
	changeGit(t, dir, "add", ".")
	changeGit(t, dir, "commit", "-m", "upstream")
	// One local clone replaces two pushes through the git transport.
	remote := t.TempDir()
	changeGit(t, dir, "clone", "--bare", "--single-branch", "--branch", "main", dir, remote)
	changeGit(t, dir, "remote", "add", "origin", remote)
	changeGit(t, dir, "update-ref", "refs/remotes/origin/main", "main")
	return e, dir, remote, ci
}

func useSyncTrunk(t *testing.T, repo, remote string) {
	t.Helper()
	changeGit(t, repo, "branch", "-M", "trunk")
	changeGit(t, remote, "branch", "-m", "main", "trunk")
	changeGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/trunk")
	changeGit(t, repo, "update-ref", "refs/remotes/origin/trunk", "trunk")
	changeGit(t, repo, "update-ref", "-d", "refs/remotes/origin/main")
	changeGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
}

func savedOperation(t *testing.T, repo string) lanesync.Operation {
	t.Helper()
	common := changeGit(t, repo, "rev-parse", "--absolute-git-dir")
	return savedOperationFor(t, common)
}
func savedOperationFor(t *testing.T, common string) lanesync.Operation {
	t.Helper()
	state, err := lanesync.New().Read()
	if err != nil {
		t.Fatal(err)
	}
	return state.Operations[common]
}
func stepSync(t *testing.T, e *testEnv) {
	t.Helper()
	if _, err := e.s.SyncTick(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// waitLocalCISaved waits for the saved result of a head, which is the evidence a
// local trial reads after its run.
func waitLocalCISaved(t *testing.T, s *Service, head string) {
	t.Helper()
	registry, ok := s.LocalCIRegistry.(*fakeLocalCI)
	if !ok {
		t.Fatal("local CI registry is not the fake")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for {
		registry.mu.Lock()
		_, saved := registry.results[head]
		registry.mu.Unlock()
		if saved {
			return
		}
		select {
		case <-registry.saved:
		case <-ctx.Done():
			t.Fatal("CI did not finish")
		}
	}
}

func finishSync(t *testing.T, e *testEnv, repo string) lanesync.Operation {
	t.Helper()
	common := changeGit(t, repo, "rev-parse", "--absolute-git-dir")
	for i := 0; i < 12; i++ {
		stepSync(t, e)
		op := savedOperationFor(t, common)
		if !lanesync.Active(op.State) {
			return op
		}
		if op.State == "testing" && runningLocalCIHead(e.s) != "" {
			waitLocalCISaved(t, e.s, op.Head)
		}
	}
	t.Fatal("operation did not finish")
	return lanesync.Operation{}
}
func TestRefreshPublishesTestedMergeAndSurvivesRestart(t *testing.T) {
	e, repo, _, _ := syncEnv(t)
	before := changeGit(t, repo, "rev-parse", "dyna/integration")
	started, err := e.s.SyncRefresh(context.Background(), e.author.RoomID, repo)
	if err != nil || !started.Started {
		t.Fatalf("start: %+v %v", started, err)
	}
	if _, err := e.s.GitExport(context.Background(), e.author.RoomID, repo); err == nil {
		t.Fatal("duplicate operation accepted")
	}
	stepSync(t, e)
	op := savedOperation(t, repo)
	if op.State != "testing" {
		t.Fatalf("operation=%+v", op)
	}
	// Start CI, then resume with a new service after the complete result was saved.
	stepSync(t, e)
	waitLocalCISaved(t, e.s, op.Head)
	resumed := *e.s
	e.s = &resumed
	op = finishSync(t, e, repo)
	if op.State != "done" {
		t.Fatalf("refresh=%+v", op)
	}
	head := changeGit(t, repo, "rev-parse", "dyna/integration")
	if head != op.Head || head == before {
		t.Fatalf("integration=%s, op=%+v", head, op)
	}
	changeGit(t, repo, "merge-base", "--is-ancestor", before, head)
	changeGit(t, repo, "merge-base", "--is-ancestor", "origin/main", head)
	if refs := changeGit(t, repo, "for-each-ref", "--format=%(refname)", "refs/heads/"+op.Branch); refs != "" {
		t.Fatal("trial ref remains")
	}
}
func TestLaneOperationsUseFetchedDefaultWithoutOriginHead(t *testing.T) {
	for _, kind := range []string{"refresh", "export"} {
		t.Run(kind, func(t *testing.T) {
			e, repo, remote, _ := syncEnv(t)
			useSyncTrunk(t, repo, remote)
			changeGit(t, repo, "config", "remote.origin.followRemoteHEAD", "never")
			upstream := changeGit(t, repo, "rev-parse", "trunk")
			changeGit(t, repo, "reset", "--hard", "trunk^")
			changeGit(t, repo, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
			changeGit(t, repo, "update-ref", "refs/remotes/origin/trunk", "trunk")
			if kind == "export" {
				fakeExportGitHub(t, e, repo, remote)
				if _, err := e.s.GitExport(context.Background(), e.author.RoomID, repo); err != nil {
					t.Fatal(err)
				}
			} else if _, err := e.s.SyncRefresh(context.Background(), e.author.RoomID, repo); err != nil {
				t.Fatal(err)
			}
			// Build once without starting CI. Inspect the exact commit CI will test.
			stepSync(t, e)
			op := savedOperation(t, repo)
			if op.State != "testing" || op.Base != upstream {
				t.Fatalf("operation did not select the fetched default: %+v; want base %s", op, upstream)
			}
			changeGit(t, repo, "merge-base", "--is-ancestor", upstream, op.Head)
		})
	}
}

func TestRefreshLeaseRejectsOutsideMove(t *testing.T) {
	e, repo, _, _ := syncEnv(t)
	if _, err := e.s.SyncRefresh(context.Background(), e.author.RoomID, repo); err != nil {
		t.Fatal(err)
	}
	stepSync(t, e)
	outside := changeGit(t, repo, "rev-parse", "main")
	changeGit(t, repo, "update-ref", "refs/heads/dyna/integration", outside)
	op := finishSync(t, e, repo)
	if op.State != "failed" || !strings.Contains(op.Error, trial.ErrMainMoved.Error()) {
		t.Fatalf("outside move=%+v", op)
	}
	if got := changeGit(t, repo, "rev-parse", "dyna/integration"); got != outside {
		t.Fatal("outside move overwritten")
	}
}
func TestRefreshConflictNotifiesRootWithPaths(t *testing.T) {
	e, repo, _, _ := syncEnv(t)
	changeGit(t, repo, "checkout", "main")
	changeCommit(t, repo, "upstream conflicting\n")
	changeGit(t, repo, "push", "origin", "main")
	changeGit(t, repo, "checkout", "-b", "integration-conflict", "dyna/integration")
	changeCommit(t, repo, "local conflicting\n")
	changeGit(t, repo, "update-ref", "refs/heads/dyna/integration", "HEAD")
	e.sent = nil
	if _, err := e.s.SyncRefresh(context.Background(), e.author.RoomID, repo); err != nil {
		t.Fatal(err)
	}
	op := finishSync(t, e, repo)
	if op.State != "conflict" || len(e.sent) != 1 || e.sent[0].Target != "" || !strings.Contains(e.sent[0].Text, "dyna/integration conflicts with origin/main") || !strings.Contains(e.sent[0].Text, "code.txt") {
		t.Fatalf("conflict=%+v, notices=%+v", op, e.sent)
	}
}
func TestSyncReadOnlyRoomRefusesStart(t *testing.T) {
	e, repo, _, _ := syncEnv(t)
	if err := e.s.Notify.Rooms.UpdatePreferences(e.author.RoomID, func(r *room.Record) error { r.GitHubAccess = "read-only"; return nil }); err != nil {
		t.Fatal(err)
	}
	for _, start := range []func(context.Context, string, string) (SyncStarted, error){e.s.SyncRefresh, e.s.GitExport} {
		if _, err := start(context.Background(), e.author.RoomID, repo); err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Fatalf("read-only=%v", err)
		}
	}
}

type exportGitHubFixture struct {
	creates, edits, pushes int
	bodies, bases          []string
	open                   bool
	failCreateOnce         bool
	failCreateAlways       bool
	loseCreateResponseOnce bool
	failPushAckOnce        bool
	failPushAlways         bool
}

func fakeExportGitHub(t *testing.T, e *testEnv, repo, remote string) *exportGitHubFixture {
	t.Helper()
	fake := &exportGitHubFixture{}
	changeGit(t, repo, "remote", "set-url", "origin", "https://github.com/owner/repo.git")
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		if filepath.Base(cmd.Path) == "git" {
			if strings.Contains(strings.Join(cmd.Args, " "), "fetch") {
				real := exec.CommandContext(ctx, "/usr/bin/git", "-C", repo, "fetch", "--prune", remote, "+refs/heads/*:refs/remotes/origin/*")
				real.Stdout, real.Stderr = cmd.Stdout, cmd.Stderr
				return real.Run()
			}
			cmd.Args = append([]string(nil), cmd.Args...)
			for i, arg := range cmd.Args {
				if arg == "https://github.com/owner/repo.git" {
					cmd.Args[i] = remote
				}
			}
			cmd.Path = "/usr/bin/git"
			if len(cmd.Args) > 1 && cmd.Args[1] == "push" {
				fake.pushes++
				if fake.failPushAlways {
					return fmt.Errorf("push failed before publication")
				}
			}
			err := cmd.Run()
			if err == nil && len(cmd.Args) > 1 && cmd.Args[1] == "push" && fake.failPushAckOnce {
				fake.failPushAckOnce = false
				return fmt.Errorf("push acknowledgement lost")
			}
			return err
		}
		joined := strings.Join(cmd.Args, " ")
		for i, arg := range cmd.Args {
			if arg == "--body-file" {
				data, err := os.ReadFile(cmd.Args[i+1])
				if err != nil {
					return err
				}
				fake.bodies = append(fake.bodies, string(data))
			}
			if arg == "--base" && i+1 < len(cmd.Args) && strings.Contains(joined, "pr create") {
				fake.bases = append(fake.bases, cmd.Args[i+1])
			}
		}
		switch {
		case strings.Contains(joined, "pr list"):
			if !fake.open {
				return json.NewEncoder(cmd.Stdout).Encode([]any{})
			}
			return json.NewEncoder(cmd.Stdout).Encode([]map[string]any{{"number": 42, "url": "https://github.com/owner/repo/pull/42"}})
		case strings.Contains(joined, "pr create"):
			if fake.failCreateOnce || fake.failCreateAlways {
				fake.failCreateOnce = false
				return fmt.Errorf("create response failed")
			}
			fake.creates++
			fake.open = true
			if fake.loseCreateResponseOnce {
				fake.loseCreateResponseOnce = false
				return fmt.Errorf("create acknowledgement lost")
			}
			fmt.Fprint(cmd.Stdout, "https://github.com/owner/repo/pull/42\n")
			return nil
		case strings.Contains(joined, "pr edit"):
			fake.edits++
			return nil
		}
		return fmt.Errorf("unexpected gh command: %v", cmd.Args)
	}
	return fake
}

func TestExportPushesTestedHeadAndUpdatesOpenPR(t *testing.T) {
	e, repo, remote, _ := syncEnv(t)
	useSyncTrunk(t, repo, remote)
	fake := fakeExportGitHub(t, e, repo, remote)
	common := changeGit(t, repo, "rev-parse", "--absolute-git-dir")
	before := changeGit(t, repo, "rev-parse", "dyna/integration")
	for i := 0; i < 2; i++ {
		if i == 1 {
			changeGit(t, repo, "checkout", "-b", "next-export", "dyna/integration")
			if err := os.WriteFile(filepath.Join(repo, "code.txt"), []byte("next"), 0600); err != nil {
				t.Fatal(err)
			}
			changeGit(t, repo, "commit", "-am", "next change")
			r, _, err := e.s.OpenChange(context.Background(), authority.Actor{Role: authority.Owner}, e.author, OpenChangeOptions{Dir: repo, Title: "Next exported change", Notes: ChangeNotes{Intent: "Add next feature", Risks: "No migration", Testing: "Focused tests"}})
			if err != nil {
				t.Fatal(err)
			}
			publishChange(t, e, repo, r)
			before = r.Head
		}

		if _, err := e.s.GitExport(context.Background(), e.author.RoomID, repo); err != nil {
			t.Fatal(err)
		}
		op := finishSync(t, e, repo)
		if op.State != "done" || op.PRURL != "https://github.com/owner/repo/pull/42" {
			t.Fatalf("export=%+v", op)
		}
		state, _ := lanesync.New().Read()
		export := state.Exports[common]
		if export.Commit != op.Head {
			t.Fatal("last export not saved")
		}
		if got := changeGit(t, remote, "rev-parse", export.Branch); got != op.Head {
			t.Fatal("export did not push tested commit")
		}
	}
	if len(fake.bases) != 1 || fake.bases[0] != "trunk" {
		t.Fatalf("export bases = %v, want trunk", fake.bases)
	}
	if fake.creates != 1 || fake.edits != 1 || len(fake.bodies) != 2 || !strings.Contains(fake.bodies[1], fake.bodies[0]) || !strings.Contains(fake.bodies[1], "Next exported change") || !strings.Contains(fake.bodies[1], "Add next feature") || !strings.Contains(fake.bodies[1], "No migration") || !strings.Contains(fake.bodies[1], "Focused tests") {
		t.Fatalf("create/edit=%d/%d bodies=%v", fake.creates, fake.edits, fake.bodies)
	}
	if got := changeGit(t, repo, "rev-parse", "dyna/integration"); got != before {
		t.Fatal("export moved integration")
	}
	for _, body := range fake.bodies {
		if !strings.Contains(body, "Intent:") || !strings.Contains(body, "Risks:") || !strings.Contains(body, "Testing:") {
			t.Fatalf("body=%s", body)
		}
	}
}

func TestReturnedLocalChangeQueueCarriesFailureDetails(t *testing.T) {
	e, _, records, _ := localMergeEnv(t, false)
	if err := e.s.refreshLocalMergeFacts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Store.update(func(st *State) error {
		st.Records[records[0].Key].MergeTrialFailure = &state.MergeTrialFailure{Change: state.MergeTrialChange{Key: records[0].Key, Head: records[0].Head}, Reason: "trial failed", Logs: "go (failed): parser failed"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, _ := e.s.Store.Read()
	rows := mergeQueue(st)
	for _, row := range rows {
		if row.ChangeID == records[0].Key {
			if row.State != "returned" || row.FailedSuite != "go" || !strings.Contains(row.Excerpt, "parser failed") || row.Title == "" || row.Author == "" {
				t.Fatalf("returned row=%+v", row)
			}
			return
		}
	}
	t.Fatal("returned change missing")
}

func readyExport(t *testing.T, e *testEnv, repo string) lanesync.Operation {
	t.Helper()
	if _, err := e.s.GitExport(context.Background(), e.author.RoomID, repo); err != nil {
		t.Fatal(err)
	}
	stepSync(t, e)
	stepSync(t, e)
	op := savedOperation(t, repo)
	if runningLocalCIHead(e.s) != "" {
		waitLocalCISaved(t, e.s, op.Head)
	}
	if op.State == "testing" {
		stepSync(t, e)
		op = savedOperation(t, repo)
	}
	if op.State != "publishing" {
		t.Fatalf("export did not reach publication: %+v", op)
	}
	return op
}
func TestExportRejectsOutsideBranchMovement(t *testing.T) {
	e, repo, remote, _ := syncEnv(t)
	fakeExportGitHub(t, e, repo, remote)
	if _, err := e.s.GitExport(context.Background(), e.author.RoomID, repo); err != nil {
		t.Fatal(err)
	}
	if op := finishSync(t, e, repo); op.State != "done" {
		t.Fatalf("first export=%+v", op)
	}
	common := changeGit(t, repo, "rev-parse", "--absolute-git-dir")
	state, _ := lanesync.New().Read()
	branch := state.Exports[common].Branch
	changeGit(t, repo, "checkout", "-b", "next-integration", "dyna/integration")
	changeCommit(t, repo, "next change\n")
	changeGit(t, repo, "update-ref", "refs/heads/dyna/integration", "HEAD")
	if _, err := e.s.GitExport(context.Background(), e.author.RoomID, repo); err != nil {
		t.Fatal(err)
	}
	stepSync(t, e)
	frozen := savedOperation(t, repo)
	if frozen.ExportBranch != branch || frozen.ExportExpected != state.Exports[common].Commit || frozen.State != "testing" {
		t.Fatalf("export lease was not saved before CI: %+v", frozen)
	}
	outside := changeGit(t, remote, "rev-parse", "main")
	changeGit(t, remote, "update-ref", "refs/heads/"+branch, outside)
	op := finishSync(t, e, repo)
	if op.State != "failed" {
		t.Fatalf("outside movement accepted: %+v", op)
	}
	if got := changeGit(t, remote, "rev-parse", branch); got != outside {
		t.Fatal("outside branch edit was overwritten")
	}
	if op.PublicationPending {
		t.Fatal("lease failure can resume publication")
	}
	if refs := changeGit(t, repo, "for-each-ref", "--format=%(refname)", "refs/heads/"+op.Branch); refs != "" {
		t.Fatal("terminal lease failure retained the trial ref")
	}
}
func TestExportNewBranchMustBeAbsent(t *testing.T) {
	e, repo, remote, _ := syncEnv(t)
	fakeExportGitHub(t, e, repo, remote)
	if _, err := e.s.GitExport(context.Background(), e.author.RoomID, repo); err != nil {
		t.Fatal(err)
	}
	stepSync(t, e)
	op := savedOperation(t, repo)
	if op.ExportExpected != "" || op.ExportBranch == "" || op.State != "testing" {
		t.Fatalf("new export lease was not saved before CI: %+v", op)
	}
	branch := strings.Replace(op.Branch, "dyna/merge-trial/", "dyna/export/", 1)
	outside := changeGit(t, remote, "rev-parse", "main")
	changeGit(t, remote, "update-ref", "refs/heads/"+branch, outside)
	op = finishSync(t, e, repo)
	if op.State != "failed" {
		t.Fatalf("existing new branch was accepted: %+v", op)
	}
	if got := changeGit(t, remote, "rev-parse", branch); got != outside {
		t.Fatal("pre-existing new branch was overwritten")
	}
}
func TestExportPublicationErrorSurvivesWorkerRestart(t *testing.T) {
	exportPublicationErrorSurvivesWorkerRestart(t, false)
}
func TestExportPublicationErrorAfterCreateSurvivesWorkerRestart(t *testing.T) {
	exportPublicationErrorSurvivesWorkerRestart(t, true)
}
func exportPublicationErrorSurvivesWorkerRestart(t *testing.T, created bool) {
	e, repo, remote, _ := syncEnv(t)
	fake := fakeExportGitHub(t, e, repo, remote)
	original := readyExport(t, e, repo)
	fake.failCreateOnce = !created
	fake.loseCreateResponseOnce = created
	stepSync(t, e)
	interrupted := savedOperation(t, repo)
	if interrupted.State != "publishing" || interrupted.Error == "" || interrupted.Attempts != 1 || interrupted.Head != original.Head || interrupted.ExportBranch != original.ExportBranch {
		t.Fatalf("publication cannot resume: %+v", interrupted)
	}
	if fake.pushes != 1 {
		t.Fatalf("first publication pushed %d times", fake.pushes)
	}
	if _, err := e.s.GitExport(context.Background(), e.author.RoomID, repo); err == nil {
		t.Fatal("a second operation can abandon unfinished publication")
	}
	calls := len(fake.bodies)
	stepSync(t, e)
	if len(fake.bodies) != calls {
		t.Fatal("publication retried before its saved delay")
	}
	resumed := *e.s
	e.s = &resumed
	e.clock = e.clock.Add(time.Minute)
	op := finishSync(t, e, repo)
	if op.State != "done" || op.Head != original.Head || op.ExportBranch != original.ExportBranch || fake.pushes != 1 || fake.creates != 1 {
		t.Fatalf("retry=%+v pushes=%d creates=%d", op, fake.pushes, fake.creates)
	}
	if created && fake.edits != 1 {
		t.Fatalf("created PR was not recovered: edits=%d", fake.edits)
	}
	branches := changeGit(t, remote, "for-each-ref", "--format=%(refname)", "refs/heads/dyna/export/")
	if branches != "refs/heads/"+original.ExportBranch {
		t.Fatalf("duplicate export branch: %s", branches)
	}
	common := changeGit(t, repo, "rev-parse", "--absolute-git-dir")
	state, _ := lanesync.New().Read()
	if saved := state.Exports[common]; saved.Commit != op.Head || saved.PRURL != op.PRURL {
		t.Fatalf("export record was not recovered: %+v", saved)
	}
}

func TestExportLostPushAcknowledgementIsIdempotent(t *testing.T) {
	e := newEnv(t)
	repo, head := changeRepo(t)
	remote := t.TempDir()
	changeGit(t, repo, "clone", "--bare", repo, remote)
	changeGit(t, repo, "remote", "add", "origin", remote)
	if err := os.MkdirAll(filepath.Join(e.s.Store.Root, "trials"), 0700); err != nil {
		t.Fatal(err)
	}
	fake := fakeExportGitHub(t, e, repo, remote)
	fake.failPushAckOnce = true
	// Start with the immutable publication inputs produced after CI. Other
	// export tests cover preparation and the transition from testing.
	op := lanesync.Operation{OldIntegration: head, ExportBranch: "dyna/export/test"}
	op.Head = head
	record := Record{RepoPath: repo, RoomID: e.author.RoomID}
	for i := 0; i < 2; i++ {
		url, err := e.s.publishLaneExport(t.Context(), record, op)
		if err != nil || url != "https://github.com/owner/repo/pull/42" {
			t.Fatalf("publication %d: url=%q err=%v", i, url, err)
		}
	}
	if fake.failPushAckOnce || fake.pushes != 1 || fake.creates != 1 || fake.edits != 1 {
		t.Fatalf("lost acknowledgement: pushes=%d creates=%d edits=%d", fake.pushes, fake.creates, fake.edits)
	}
	if got := changeGit(t, remote, "rev-parse", op.ExportBranch); got != op.Head {
		t.Fatalf("published head=%s, want tested head %s", got, op.Head)
	}
	saved, err := lanesync.New().Read()
	if err != nil {
		t.Fatal(err)
	}
	if export := saved.Exports[repo]; export.Commit != head || export.Branch != op.ExportBranch || export.PRURL != "https://github.com/owner/repo/pull/42" {
		t.Fatalf("export record=%+v", export)
	}
}

func TestExportPublicationStopsAfterFiveCreateFailuresAndCanResume(t *testing.T) {
	exportPublicationStopsAfterFiveFailures(t, false)
}
func TestExportPublicationStopsAfterFivePushFailuresAndCanResume(t *testing.T) {
	exportPublicationStopsAfterFiveFailures(t, true)
}
func exportPublicationStopsAfterFiveFailures(t *testing.T, beforePush bool) {
	e, repo, remote, _ := syncEnv(t)
	common := changeGit(t, repo, "rev-parse", "--absolute-git-dir")
	fake := fakeExportGitHub(t, e, repo, remote)
	original := readyExport(t, e, repo)
	fake.failCreateAlways = !beforePush
	fake.failPushAlways = beforePush
	for attempt := 1; attempt <= 5; attempt++ {
		stepSync(t, e)
		op := savedOperationFor(t, common)
		want := "publishing"
		if attempt == 5 {
			want = "failed"
		}
		if op.State != want || op.Error == "" || op.Attempts != attempt {
			t.Fatalf("attempt %d: %+v", attempt, op)
		}
		if attempt == 2 {
			resumed := *e.s
			e.s = &resumed
		}
		e.clock = e.clock.Add(time.Minute)
	}
	calls := len(fake.bodies)
	if beforePush {
		calls = fake.pushes
	}
	stepSync(t, e)
	if calls != 5 || (beforePush && fake.pushes != calls) || (!beforePush && len(fake.bodies) != calls) {
		t.Fatalf("publication did not stop after five failures: %d calls", len(fake.bodies))
	}
	if op := savedOperationFor(t, common); !op.PublicationPending {
		t.Fatal("unfinished publication inputs can be discarded")
	}
	if _, err := e.s.SyncRefresh(context.Background(), e.author.RoomID, repo); err == nil {
		t.Fatal("refresh can discard unfinished export publication")
	}
	if got := changeGit(t, repo, "for-each-ref", "--format=%(objectname)", "refs/heads/"+original.Branch); got != original.Head {
		t.Fatalf("pending publication lost its trial ref: %s", got)
	}
	changeGit(t, repo, "reflog", "expire", "--expire=now", "--all")
	changeGit(t, repo, "gc", "--prune=now")
	changeGit(t, repo, "cat-file", "-e", original.Head+"^{commit}")
	fake.failCreateAlways = false
	fake.failPushAlways = false
	if _, err := e.s.GitExport(context.Background(), e.author.RoomID, repo); err != nil {
		t.Fatal(err)
	}
	resumed := savedOperationFor(t, common)
	if resumed.State != "publishing" || resumed.ExportBranch != original.ExportBranch || resumed.Head != original.Head {
		t.Fatalf("manual retry abandoned publication: %+v", resumed)
	}
	restarted := *e.s
	e.s = &restarted
	done := finishSync(t, e, repo)
	wantPushes := 1
	if beforePush {
		wantPushes = 6
	}
	if done.State != "done" || fake.creates != 1 || fake.pushes != wantPushes {
		t.Fatalf("manual retry: %+v, creates=%d pushes=%d", done, fake.creates, fake.pushes)
	}
	if refs := changeGit(t, repo, "for-each-ref", "--format=%(refname)", "refs/heads/"+original.Branch); refs != "" {
		t.Fatal("completed export retained the trial ref")
	}
}

// SyncTick holds its operation lease across git and network work, but no
// writer waits on that lease: while a pass is held inside its fetch, the lane
// sync store and the PR store still take writes (item 6).
func TestSyncTickGitWorkBlocksNoWriter(t *testing.T) {
	e, repo, _, _ := syncEnv(t)
	if _, err := e.s.SyncRefresh(context.Background(), e.author.RoomID, repo); err != nil {
		t.Fatal(err)
	}
	// A GitHub origin sends the fetch through the test's command hook.
	changeGit(t, repo, "remote", "set-url", "origin", "https://github.com/acme/widgets.git")
	entered, release := make(chan struct{}), make(chan struct{})
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		if slices.Contains(cmd.Args, "fetch") {
			close(entered)
			<-release
		}
		return errors.New("the test stopped this command")
	}
	done := make(chan error, 1)
	go func() { _, err := e.s.SyncTick(context.Background()); done <- err }()
	defer func() { close(release); <-done }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("SyncTick never reached its fetch")
	}
	for name, write := range map[string]func() error{
		"lane sync": func() error { return lanesync.New().Update(func(*lanesync.Snapshot) error { return nil }) },
		"PR store":  func() error { return e.s.Store.update(func(*State) error { return nil }) },
	} {
		wrote := make(chan error, 1)
		go func() { wrote <- write() }()
		select {
		case err := <-wrote:
			if err != nil {
				t.Fatalf("%s write: %v", name, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("a %s write waited on SyncTick's git work", name)
		}
	}
}
