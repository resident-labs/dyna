package watch

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/machines"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// trialStartEnv is a local-mode room with one merge trial whose head already
// has a passing hosted push run. The base definition still requires local
// suites that hosted CI never reports, so the trial gate holds the head until
// local CI runs. nodeSuites limits the one CI machine, and clock is the
// service's injected time.
func trialStartEnv(t *testing.T, nodeSuites []string) (s *Service, r Record, branch, repoKey string, sent *[]Notice, clock *time.Time) {
	t.Helper()
	ctx := context.Background()
	dataDir := t.TempDir()
	repoDir, base, head, prHead := trialRepoWithDefinition(t)
	// A repository with no remote counts as the local merge lane; this trial is
	// a hosted pull request that the room's local CI checks.
	changeGit(t, repoDir, "remote", "add", "origin", "https://github.com/owner/repo.git")
	registry := localci.Store{DataDir: dataDir}
	if _, err := (machines.Store{DataDir: dataDir}).Save(ctx, machines.Machine{Name: "worker", Roles: []machines.Role{machines.RoleCI}, SSH: "ari@worker", CISlots: 1, Enabled: true, Suites: nodeSuites}); err != nil {
		t.Fatal(err)
	}
	rooms := &room.Store{Root: t.TempDir()}
	rm, err := rooms.Create(room.CreateInput{Name: "trial-start"})
	if err != nil {
		t.Fatal(err)
	}
	if err := rooms.UpdatePreferences(rm.ID, func(rec *room.Record) error { rec.GitHubAccess = "read-write"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := registry.SetMode(ctx, rm.ID, localci.ModeLocal); err != nil {
		t.Fatal(err)
	}
	branch = "dyna/merge-trial/894"
	r = Record{Kind: "github", Key: "pr", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Head: prHead, Base: base, Branch: "feature", RepoPath: repoDir, RoomID: rm.ID, State: "open", Watching: true, CI: "passed", Polling: Polling{PullHead: prHead}}
	at := time.Date(2026, 10, 28, 14, 0, 0, 0, time.UTC)
	notices := []Notice{}
	clock = &at
	s = &Service{Store: &Store{Root: state.Root(dataDir)}, LocalCIRegistry: registry, Notify: notify.New(rooms, subagent.NewManager()), Now: func() time.Time { return at }}
	s.Deliver = func(_ context.Context, _ Record, n Notice) (string, error) {
		notices = append(notices, n)
		return "msg", nil
	}
	_ = sent
	sent = &notices
	repoKey = defaultRepo(r)
	if err := s.Store.update(func(st *State) error {
		st.Records[r.Key] = &r
		st.RepositoryFacts = map[string]state.RepositoryFacts{repoKey: {Head: base, DefaultBranch: "main"}}
		st.MergeTrials = map[string]*state.MergeTrial{repoKey: {Base: base, Head: head, Branch: branch, DefaultBranch: "main", Phase: state.MergeTrialPhaseRunning, Created: at, Changes: []state.MergeTrialChange{{Key: r.Key, Head: prHead}}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The hosted read sees one green push run for the trial branch. It names
	// none of the local suites the base definition requires.
	run := Run{ID: 81, Attempt: 1, CheckSuiteID: 9, Path: ".github/workflows/ci.yml", HeadSHA: head, HeadBranch: branch, Event: "push", Status: "completed", Conclusion: "success"}
	check := actionsCheck(Check{ID: 7, Name: "unit", Status: "completed", Conclusion: "success"})
	s.GH.run = func(_ context.Context, cmd *exec.Cmd) error {
		if filepath.Base(cmd.Path) == "git" {
			return nil
		}
		endpoint := ""
		for _, arg := range cmd.Args {
			if strings.HasPrefix(arg, "repos/") {
				endpoint = arg
			}
		}
		switch {
		case strings.Contains(endpoint, "/check-runs?"):
			return writeFakeJSON(cmd.Stdout, checkRunsResponse{CheckRuns: []Check{check}})
		case strings.Contains(endpoint, "/statuses?"):
			return writeFakeJSON(cmd.Stdout, []Status{})
		case strings.Contains(endpoint, "/actions/runs?"):
			return writeFakeJSON(cmd.Stdout, workflowRunsResponse{WorkflowRuns: []Run{run}})
		default:
			return fmt.Errorf("unexpected endpoint %s", endpoint)
		}
	}
	return s, r, branch, repoKey, sent, clock
}

// trialStartHead reads the stored trial's head.
func trialStartHead(t *testing.T, s *Service, repoKey string) (b state.MergeTrial) {
	t.Helper()
	st, err := s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if batch := st.MergeTrials[repoKey]; batch != nil {
		b = *batch
	}
	return b
}

// Hosted CI that passed without a trial's local suites settles nothing: the
// scheduler must still admit the local run for the trial head. Trial 894
// waited seventeen minutes because the settled check judged the hosted read
// with no required checks at all (D-407).
func TestTrialLocalCIStartsWhenHostedPassLacksItsLocalChecks(t *testing.T) {
	ctx := context.Background()
	s, r, _, repoKey, _, _ := trialStartEnv(t, nil)
	st, err := s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	s.RunNodeCI = func(runCtx context.Context, _ localci.Node, _ localci.Request) (localci.Result, error) {
		<-runCtx.Done()
		return localci.Result{}, runCtx.Err()
	}
	if err := s.scheduleRegisteredCI(ctx, st); err != nil {
		t.Fatal(err)
	}
	head := trialStartHead(t, s, repoKey).Head
	job := registeredCIJob{record: r, head: head, branch: trialStartHead(t, s, repoKey).Branch, trial: true}
	if !admittedRun(s, job) {
		t.Fatal("scheduler settled the trial head on a hosted pass, so its required local run never started")
	}
	if left := s.StopLocalCI(ctx); len(left) > 0 {
		t.Fatalf("runs still working: %v", left)
	}
}

// A trial head that waits for local CI no machine starts gets one bounded
// retry after a few minutes, then exactly one typed root alert. The clock is
// injected, so the whole wait takes no time (D-407).
func TestTrialRetriesOnceAndAlertsRootWhenLocalCINeverStarts(t *testing.T) {
	ctx := context.Background()
	// The one CI machine allows only the relay suite, so the whole-suite trial
	// run is blocked: local CI never starts, and the wait needs its bound.
	s, _, _, repoKey, sent, clock := trialStartEnv(t, []string{"relay"})
	advance := func() {
		t.Helper()
		b := trialStartHead(t, s, repoKey)
		if err := s.advanceMergeTrial(ctx, repoKey, b); err != nil {
			t.Fatal(err)
		}
	}
	rootAlerts := func() []Notice {
		var out []Notice
		for _, n := range *sent {
			if n.NeedsRoot && n.Merge != nil && n.Merge.Type == room.MergeChecks {
				out = append(out, n)
			}
		}
		return out
	}

	advance()
	b := trialStartHead(t, s, repoKey)
	if b.LocalCIWait == nil || !b.LocalCIWait.Since.Equal(*clock) {
		t.Fatalf("unstarted wait not recorded: %+v", b.LocalCIWait)
	}
	*clock = clock.Add(2 * time.Minute)
	advance()
	b = trialStartHead(t, s, repoKey)
	if b.LocalCIWait.Retried {
		t.Fatal("wait recorded a retry before its window passed")
	}
	if got := rootAlerts(); len(got) != 0 {
		t.Fatalf("alerted before the retry window: %d", len(got))
	}
	*clock = clock.Add(time.Minute + time.Second)
	advance()
	b = trialStartHead(t, s, repoKey)
	if !b.LocalCIWait.Retried {
		t.Fatal("the bounded retry did not run after its window")
	}
	if got := rootAlerts(); len(got) != 0 {
		t.Fatalf("alerted before the alert window: %d", len(got))
	}
	*clock = clock.Add(3*time.Minute + time.Second)
	advance()
	b = trialStartHead(t, s, repoKey)
	if !b.LocalCIWait.Alerted {
		t.Fatal("root was not alerted after the retry started nothing")
	}
	got := rootAlerts()
	if len(got) != 1 || got[0].Merge.Reason != "local CI has not started" {
		t.Fatalf("root alerts = %d, want exactly one typed alert", len(got))
	}
	*clock = clock.Add(30 * time.Minute)
	advance()
	if got := rootAlerts(); len(got) != 1 {
		t.Fatalf("root was alerted %d times, want one", len(got))
	}
}
