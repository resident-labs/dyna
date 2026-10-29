package watch

import (
	"context"
	"strings"
	"testing"

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
	wanted := localci.OrphanedAttachment{Repo: "/repo", SHA: wantedSHA, Node: "node", RunID: "wanted"}
	unwanted := localci.OrphanedAttachment{Repo: "/repo", SHA: unwantedSHA, Node: "node", RunID: "unwanted"}
	abandoned := make(chan localci.OrphanedAttachment, 2)
	registry := &orphanedRunRegistry{orphans: []localci.OrphanedAttachment{wanted, unwanted}, abandoned: abandoned}
	runs := &registeredCIRuns{runs: map[string]*registeredCIRun{}}
	// The tick still decides which jobs it wants, but closed admission keeps this
	// test on the orphan rule without preparing or starting a replacement run.
	runs.closed.Store(true)
	s := &Service{LocalCIRegistry: registry, registeredRuns: runs}
	record := &Record{Kind: "local", Key: "change", Repo: "owner/repo", Head: wantedSHA, RepoPath: t.TempDir(), Watching: true, State: state.PROpen}
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
