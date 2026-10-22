package watch

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/pr/state"
	appstate "dyna-agent/internal/state"
	"dyna-agent/internal/store/storetest"
)

func TestMergeHistoryRecordsPassedTrialAndPublish(t *testing.T) {
	e, dir, records, ci := localMergeEnvFor(t, false, "good")
	localTick(t, e)
	finishLocalTrials(t, e, ci, false)
	history, err := e.s.RoomMergeHistory(e.author.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	main := changeGit(t, dir, "rev-parse", "dyna/integration")
	if len(history) != 2 || history[0].Kind != "publish" || history[0].Outcome != "published" || history[0].Head != main ||
		history[1].Kind != "trial" || history[1].Outcome != "passed" || len(history[0].Changes) != 1 || history[0].Changes[0] != changeLabel(records[0]) {
		t.Fatalf("history = %+v (main %s)", history, main)
	}
}

func TestMergeHistoryRecordsFailedTrialWithDetail(t *testing.T) {
	e, _, _, ci := localMergeEnvFor(t, true, "bad")
	localTick(t, e)
	finishLocalTrials(t, e, ci, true)
	history, err := e.s.RoomMergeHistory(e.author.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range history {
		if entry.Kind == "trial" && entry.Outcome == "failed" && entry.Detail != "" {
			return
		}
	}
	t.Fatalf("no failed trial with detail: %+v", history)
}

func TestMergeHistoryIgnoresRepeatsAndCapsReads(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	b := state.MergeTrial{Branch: "dyna/merge-trial/1", Changes: []state.MergeTrialChange{{Key: r.Key, Head: r.Head}}}
	for range 2 {
		if err := e.s.recordMergeHistory(st, "github.com/owner/repo", b, "trial", "passed", "aaaa", ""); err != nil {
			t.Fatal(err)
		}
	}
	if history, _ := e.s.RoomMergeHistory(r.RoomID); len(history) != 1 {
		t.Fatalf("a repeated step was recorded twice: %+v", history)
	}
	for i := range mergeHistoryRead + 5 {
		if err := e.s.recordMergeHistory(st, "github.com/owner/repo", b, "trial", "failed", fmt.Sprint(i), ""); err != nil {
			t.Fatal(err)
		}
	}
	history, _ := e.s.RoomMergeHistory(r.RoomID)
	if len(history) != mergeHistoryRead || history[0].Head != fmt.Sprint(mergeHistoryRead+4) {
		t.Fatalf("read is not capped newest first: %d rows, first %+v", len(history), history[0])
	}
}

func historyRow(history []MergeHistoryEntry, kind, outcome string, changes ...string) *MergeHistoryEntry {
	for i := range history {
		if history[i].Kind == kind && history[i].Outcome == outcome && slices.Equal(history[i].Changes, changes) {
			return &history[i]
		}
	}
	return nil
}

func TestMergeHistoryRecordsPassingPrefix(t *testing.T) {
	mergeHistoryPrefix(t, "passed", "good", "bad")
}

func TestMergeHistoryRecordsFailingPrefix(t *testing.T) {
	mergeHistoryPrefix(t, "failed", "bad", "later")
}

// mergeHistoryPrefix bisects a failed two-change batch. want is the outcome of
// its one-change prefix.
func mergeHistoryPrefix(t *testing.T, want string, names ...string) {
	e, _, records, ci := localMergeEnvFor(t, true, names...)
	localTick(t, e)
	finishLocalTrials(t, e, ci, true)
	history, err := e.s.RoomMergeHistory(e.author.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	full := historyRow(history, "trial", "failed", changeLabel(records[0]), changeLabel(records[1]))
	prefix := historyRow(history, "trial", want, changeLabel(records[0]))
	if full == nil || prefix == nil || prefix.Branch == full.Branch || prefix.Head == full.Head {
		t.Fatalf("prefix history missing: %+v", history)
	}
	if want == "passed" && historyRow(history, "publish", "published", changeLabel(records[0])) == nil {
		t.Fatalf("published prefix missing: %+v", history)
	}
}

// Publication checks the prefix CI again. Its failure keeps the prefix's own
// branch, head and changes, before the trial returns to the full batch.
func TestMergeHistoryKeepsPrefixOnRevalidationFailure(t *testing.T) {
	e, _, records, ci := localMergeEnvFor(t, true, "good", "bad")
	repo := defaultRepo(records[0])
	prefixHead := strings.Repeat("e", 40)
	var suites []localci.SuiteResult
	for _, name := range localci.DefaultSuites() {
		suites = append(suites, localci.SuiteResult{Suite: name, Status: "failed"})
	}
	ci.results[prefixHead] = localci.Result{Repo: records[0].RepoPath, SHA: prefixHead, Status: "failed", Suites: suites, Error: "prefix changed"}
	good := state.MergeTrialChange{Key: records[0].Key, Head: records[0].Head}
	bad := state.MergeTrialChange{Key: records[1].Key, Head: records[1].Head}
	b := state.MergeTrial{Phase: "publishing", Branch: "dyna/merge-trial/9", Head: prefixHead, Changes: []state.MergeTrialChange{good},
		StackChanges: []state.MergeTrialChange{good, bad}, RootBranch: "dyna/merge-trial/8", OriginalHead: strings.Repeat("f", 40),
		Stacks: []state.MergeTrialStack{{Branch: "dyna/merge-trial/9", Count: 1, Phase: "passed", Head: prefixHead}}, Bisect: true, Created: e.clock}
	if err := e.s.Store.update(func(st *State) error {
		if st.MergeTrials == nil {
			st.MergeTrials = map[string]*state.MergeTrial{}
		}
		st.MergeTrials[repo] = &b
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.advanceMergeTrial(t.Context(), repo, b); err != nil {
		t.Fatal(err)
	}
	history, err := e.s.RoomMergeHistory(e.author.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	row := historyRow(history, "trial", "failed", changeLabel(records[0]))
	if row == nil || row.Branch != "dyna/merge-trial/9" || row.Head != prefixHead || row.Detail == "" {
		t.Fatalf("revalidation failure lost the prefix identity: %+v", history)
	}
}

// A merge history row is written through the process's one writer, so the
// engine counts it in Health and orders it with every other write.
func TestMergeHistoryWritesThroughTheWriter(t *testing.T) {
	storetest.DataDir(t)
	s := &Service{Store: &Store{Root: state.Root(t.TempDir())}}
	db, err := s.mergeHistoryDB()
	if err != nil {
		t.Fatal(err)
	}
	before := appstate.For(db).Health().Writes
	if err := s.saveMergeHistory(db, MergeHistoryEntry{Kind: "trial", Outcome: "passed", Repo: "owner/repo", Branch: "b", Head: "h", Changes: []string{}}, nil); err != nil {
		t.Fatal(err)
	}
	if got := appstate.For(db).Health().Writes - before; got != 1 {
		t.Fatalf("a merge history row counted %d writes, want 1", got)
	}
}
