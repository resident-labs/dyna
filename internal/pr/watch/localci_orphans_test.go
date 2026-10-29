package watch

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"dyna-agent/internal/jobs"
	"dyna-agent/internal/localci"
	"dyna-agent/internal/machines"
	"dyna-agent/internal/pr/state"
)

type orphanedRunRegistry struct {
	localCIRegistry
	orphans   []localci.OrphanedAttachment
	abandoned chan<- localci.OrphanedAttachment
}

func (r *orphanedRunRegistry) Nodes(context.Context) ([]localci.Node, error) {
	return []localci.Node{{Name: "node", Enabled: true, Labels: []string{machines.LabelCI}}}, nil
}

func (r *orphanedRunRegistry) Mode(context.Context, string) (string, error) {
	return localci.ModeLocal, nil
}

func (r *orphanedRunRegistry) LoadResult(context.Context, string, string) (localci.Result, error) {
	// A conclusive saved result lets the tick finish without preparing a run.
	return localci.Result{Node: "node", Status: localci.OutcomeFailed}, nil
}

func (r *orphanedRunRegistry) OrphanedAttachments(context.Context) ([]localci.OrphanedAttachment, error) {
	return r.orphans, nil
}

func (r *orphanedRunRegistry) AbandonOrphanedRun(_ context.Context, orphan localci.OrphanedAttachment) error {
	r.abandoned <- orphan
	return nil
}

func TestScheduleRegisteredCIAbandonsOnlyUnwantedOrphans(t *testing.T) {
	wantedSHA := strings.Repeat("a", 40)
	unwantedSHA := strings.Repeat("b", 40)
	schedulerSource := jobs.CheckSource{Kind: jobs.CheckKindPullRequest, Trial: true}
	wanted := localci.OrphanedAttachment{Repo: "/repo", SHA: wantedSHA, Node: "node", RunID: "wanted", Source: schedulerSource}
	unwanted := localci.OrphanedAttachment{Repo: "/repo", SHA: unwantedSHA, Node: "node", RunID: "unwanted", Source: schedulerSource}
	syncRun := localci.OrphanedAttachment{Repo: "/repo", SHA: strings.Repeat("c", 40), Node: "node", RunID: "sync", Source: jobs.CheckSource{Kind: jobs.CheckKindSync}}
	standalone := localci.OrphanedAttachment{Repo: "/repo", SHA: strings.Repeat("d", 40), Node: "node", RunID: "standalone", Source: jobs.CheckSource{Kind: jobs.CheckKindLocalChange}}
	legacy := localci.OrphanedAttachment{Repo: "/repo", SHA: strings.Repeat("e", 40), Node: "node", RunID: "legacy"}
	abandoned := make(chan localci.OrphanedAttachment, 5)
	registry := &orphanedRunRegistry{orphans: []localci.OrphanedAttachment{wanted, unwanted, syncRun, standalone, legacy}, abandoned: abandoned}
	s := &Service{
		LocalCIRegistry: registry,
		registeredRuns:  &registeredCIRuns{runs: map[string]*registeredCIRun{}},
		SyncRunActive: func(repo, sha string) (bool, error) {
			return repo == syncRun.Repo && sha == syncRun.SHA, nil
		},
	}
	repo := t.TempDir()
	if raw, err := exec.Command("git", "init", "--quiet", repo).CombinedOutput(); err != nil {
		t.Fatalf("init repository: %v: %s", err, raw)
	}
	record := &Record{Kind: "github", Key: "pr", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Head: wantedSHA, RepoPath: repo, Watching: true, State: state.PROpen}
	st := State{Records: map[string]*Record{record.Key: record}, MergeTrials: map[string]*state.MergeTrial{
		defaultRepo(*record): {Phase: state.MergeTrialPhaseRunning, Branch: "dyna/merge-trial/1", Head: wantedSHA, Changes: []state.MergeTrialChange{{Key: record.Key, Head: wantedSHA}}},
	}}
	if err := s.scheduleRegisteredCI(t.Context(), st); err != nil {
		t.Fatal(err)
	}
	if got := <-abandoned; got != unwanted {
		t.Fatalf("abandoned %+v, want only %+v", got, unwanted)
	}
	select {
	case got := <-abandoned:
		t.Fatalf("abandoned an extra run: %+v", got)
	default:
	}
}
