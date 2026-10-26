package maintenance_test

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/localci"
	"dyna-agent/internal/maintenance"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/store/storetest"
)

// The pass deletes old local CI results but keeps an open PR's head.
func TestPassKeepsLocalCIResultOfAnOpenPRHead(t *testing.T) {
	dataDir := storetest.Isolate(t)
	t.Cleanup(func() { database.Close(dataDir) })
	t.Setenv("DYNA_ARCHIVE_DIR", "")
	ctx := context.Background()
	store := localci.Store{DataDir: dataDir}
	const repo = "github.com/acme/widgets"
	head, stale := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for _, sha := range []string{head, stale} {
		if err := store.SaveResult(ctx, localci.Result{Repo: repo, SHA: sha, Status: localci.OutcomePassed}); err != nil {
			t.Fatal(err)
		}
	}
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE local_ci_results SET recorded_at=1"); err != nil {
		t.Fatal(err)
	}
	record := &state.Record{Key: repo + "#1", Repo: repo, Number: 1, State: "open", Head: head}
	if err := state.Write(state.Root(dataDir), state.State{Version: 1, Records: map[string]*state.Record{record.Key: record}}); err != nil {
		t.Fatal(err)
	}
	result, err := maintenance.Pass(ctx, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Pruned["local CI results"].Items; got != 1 {
		t.Fatalf("pass removed %d local CI results, want 1", got)
	}
	if _, err := store.LoadResult(ctx, repo, head); err != nil {
		t.Fatalf("the open PR head's result was removed: %v", err)
	}
	if _, err := store.LoadResult(ctx, repo, stale); err == nil {
		t.Fatal("the stale result stayed")
	}
}

// A paused merge trial resumes from the result saved for the commit it tested,
// which is the trial's head, not a source change's head.
func TestPassKeepsLocalCIResultOfAnUnpublishedTrialHead(t *testing.T) {
	dataDir := storetest.Isolate(t)
	t.Cleanup(func() { database.Close(dataDir) })
	t.Setenv("DYNA_ARCHIVE_DIR", "")
	ctx := context.Background()
	store := localci.Store{DataDir: dataDir}
	const repo = "github.com/acme/widgets"
	trialHead, stackHead := strings.Repeat("c", 40), strings.Repeat("d", 40)
	for _, sha := range []string{trialHead, stackHead} {
		if err := store.SaveResult(ctx, localci.Result{Repo: repo, SHA: sha, Status: localci.OutcomePassed}); err != nil {
			t.Fatal(err)
		}
	}
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE local_ci_results SET recorded_at=1"); err != nil {
		t.Fatal(err)
	}
	trial := &state.MergeTrial{Head: trialHead, Stacks: []state.MergeTrialStack{{Head: stackHead}}, Phase: state.MergeTrialPhaseAwaitingRoot}
	if err := state.Write(state.Root(dataDir), state.State{Version: 1, MergeTrials: map[string]*state.MergeTrial{repo: trial}}); err != nil {
		t.Fatal(err)
	}
	if _, err := maintenance.Pass(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, sha := range []string{trialHead, stackHead} {
		if _, err := store.LoadResult(ctx, repo, sha); err != nil {
			t.Fatalf("result of the unpublished trial's commit %s… was removed: %v", sha[:1], err)
		}
	}
}
