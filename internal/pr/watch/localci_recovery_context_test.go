package watch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/pr/state"
)

type recoveryContextRegistry struct {
	localCIRegistry
	result localci.Result
}

func (r *recoveryContextRegistry) Mode(ctx context.Context, _ string) (string, error) {
	return localci.ModeLocal, ctx.Err()
}

func (r *recoveryContextRegistry) LoadHostedRuns(context.Context, string, string) (localci.HostedRuns, error) {
	return localci.HostedRuns{}, os.ErrNotExist
}

func (r *recoveryContextRegistry) SaveResult(ctx context.Context, result localci.Result) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.result = result
	return nil
}

func TestRunningTrialRetriesSavedStopOnStartup(t *testing.T) {
	s, r, _, repoKey, _, _ := trialStartEnv(t, nil)
	batch := trialStartHead(t, s, repoKey)
	repo, err := localci.CanonicalRepo(t.Context(), r.RepoPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ciRegistry().SaveResult(t.Context(), localci.Result{
		Repo: repo, SHA: batch.Head, RoomID: r.RoomID, Node: "worker",
		Status: localci.OutcomeError, Cause: localci.CauseEngineStop,
	}); err != nil {
		t.Fatal(err)
	}
	s.RunNodeCI = func(ctx context.Context, _ localci.Node, _ localci.Request) (localci.Result, error) {
		<-ctx.Done()
		return localci.Result{}, ctx.Err()
	}
	// The merge worker gets this tick at engine startup, without a new result event.
	_, tickErr := s.MergeTick(t.Context())
	job := registeredCIJob{record: r, head: batch.Head, branch: batch.Branch, trial: true}
	started := admittedRun(s, job)
	s.StopLocalCI(t.Context())
	// The fixture does not implement the unrelated PR refresh endpoints.
	if !started {
		t.Fatalf("startup did not retry saved stop; tick error: %v", tickErr)
	}
}

func TestEngineStopRecordsTrialWithCancelledContext(t *testing.T) {
	registry := &recoveryContextRegistry{}
	s := &Service{Store: &Store{Root: filepath.Join(t.TempDir(), "prs")}, LocalCIRegistry: registry}
	head := strings.Repeat("a", 40)
	record := Record{Key: "pr", Kind: "github", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Head: head, State: "open", Watching: true}
	job := registeredCIJob{record: record, head: head, trial: true}
	if err := s.Store.update(func(st *State) error {
		st.Records[record.Key] = &record
		st.MergeTrials = map[string]*state.MergeTrial{defaultRepo(record): {Phase: state.MergeTrialPhaseRunning, Head: head, Changes: []state.MergeTrialChange{{Key: record.Key, Head: head}}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	localci.MarkEngineStopping()
	t.Cleanup(localci.ClearEngineStopping)
	if err := s.recoverInterruptedCI(ctx, job, "repo", localci.Node{Name: "node"}, localci.Result{}); err != nil {
		t.Fatal(err)
	}
	if registry.result.Cause != localci.CauseEngineStop || registry.result.SHA != head {
		t.Fatalf("saved result = %+v", registry.result)
	}
}
