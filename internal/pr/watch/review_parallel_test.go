package watch

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A reviewer starts on a pushed head while CI runs. Its verdict does not wait
// for CI. Dyna reports ready only for approval and green CI on the same head.
func TestReviewRunsInParallelWithCI(t *testing.T) {
	e := newEnv(t)
	e.f.Checks = []Check{actionsCheck(Check{ID: 1, Name: "go-test", Status: "in_progress"})}
	e.f.Runs = nil
	e.write()
	e.track()
	if err := e.s.Configure(e.author.RoomID, Reviewer{Profile: "judge"}); err != nil {
		t.Fatal(err)
	}
	e.tick()
	r := e.record()
	if r.CI != "pending" || len(e.launches) != 1 || r.Reviewer == "" {
		t.Fatalf("review waited for CI: %+v launches=%d", r, len(e.launches))
	}
	if err := e.s.Verdict(context.Background(), r.Key, r.Reviewer, r.Head, "approve", "Checked file.go:1", 0); err != nil {
		t.Fatalf("verdict waited for CI: %v", err)
	}
	landed := func() bool {
		for _, n := range e.sent {
			if strings.Contains(n.Text, "is ready to land") {
				return true
			}
		}
		return false
	}
	if landed() {
		t.Fatal("ready before CI passed")
	}
	e.f.Checks[0].Status, e.f.Checks[0].Conclusion = "completed", "success"
	e.tick()
	if !landed() {
		t.Fatalf("approval and green CI did not report ready: %+v", e.sent)
	}
}

// Known failed CI holds review need until a rerun passes or starts.
func TestFailedCIBlocksReviewNeed(t *testing.T) {
	e := newEnv(t)
	e.track()
	e.tick()
	r := e.record()
	if r.CI != "failed" || r.Reviewer != "" || len(e.launches) != 0 {
		t.Fatal("failed CI created review need")
	}
	e.f.Checks[0].Status, e.f.Checks[0].Conclusion = "in_progress", ""
	e.f.Runs = nil
	e.tick()
	if e.record().Reviewer == "" || len(e.launches) != 1 {
		t.Fatal("CI rerun did not restore review need")
	}
}

// A verdict reads fresh CI. A rerun that the watcher has not polled yet must
// not make an approval ready; readiness arrives with the passing poll.
func TestVerdictReadinessUsesFreshCI(t *testing.T) {
	e := newEnv(t)
	e.setCI("success", 1)
	e.write()
	e.track()
	if err := e.s.Configure(e.author.RoomID, Reviewer{Profile: "judge"}); err != nil {
		t.Fatal(err)
	}
	e.tick()
	r := e.record()
	if r.CI != "passed" || r.Reviewer == "" {
		t.Fatalf("setup: %+v", r)
	}
	e.f.Checks = []Check{actionsCheck(Check{ID: 2, Name: "test / unit", Status: "in_progress"})}
	e.f.Runs = []Run{{ID: 99, WorkflowID: 1, Name: "CI", Attempt: 2, Status: "in_progress"}}
	e.write()
	if err := e.s.Verdict(context.Background(), r.Key, r.Reviewer, r.Head, "approve", "Checked file.go:1", 0); err != nil {
		t.Fatal(err)
	}
	ready := func() bool {
		for _, n := range e.sent {
			if strings.Contains(n.Text, "is ready to land") || strings.Contains(n.Text, "Ready to merge") {
				return true
			}
		}
		return false
	}
	if ready() {
		t.Fatal("stale passing CI made the approval ready")
	}
	e.setCI("success", 3)
	e.tick()
	if !ready() {
		t.Fatalf("passing poll did not report ready: %+v", e.record())
	}
}

// Reviewers read the diff and never run the test suite; CI is the oracle.
func TestReviewerPromptNeverRunsTests(t *testing.T) {
	e := newEnv(t)
	e.track()
	task, err := e.s.prepareReviewTask(context.Background(), e.record(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Never run the test suite", "CI is the test oracle", "go vet", "Note the CI result", "approve before CI finishes"} {
		if !strings.Contains(task, want) {
			t.Errorf("prompt misses %q", want)
		}
	}
	for _, banned := range []string{"dyna check go-test", "import-github", "If evidence is missing or doubtful"} {
		if strings.Contains(task, banned) {
			t.Errorf("prompt still says %q", banned)
		}
	}
}

// Draft is a review hold. The same head becomes eligible when GitHub clears it.
func TestDraftReviewWaitsForReadyTransition(t *testing.T) {
	e := newEnv(t)
	e.setCI("success", 1)
	e.f.Draft = true
	e.write()
	tracked := e.track()
	if !tracked.IsDraft {
		t.Fatal("tracking lost the draft hold")
	}
	e.tick()
	r := e.record()
	status, err := e.s.ReviewerStatus(r.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsDraft || r.Reviewer != "" || len(e.launches) != 0 || len(status.Queue) != 0 {
		t.Fatalf("draft entered the pool: record=%+v status=%+v launches=%d", r, status, len(e.launches))
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	state, position, reason := reviewState(t, e, st, r)
	if state != "blocked" || position != nil || reason != "The PR is a draft." {
		t.Fatalf("draft status = %s %v %q", state, position, reason)
	}
	e.f.Draft = false
	e.tick()
	ready := e.record()
	if ready.IsDraft || ready.Head != r.Head || ready.Reviewer == "" || len(e.launches) != 1 {
		t.Fatalf("leaving draft did not start review on the same head: %+v", ready)
	}
	// Returning to draft releases the slot on the next reconciliation.
	reviewer := ready.Reviewer
	e.f.Draft = true
	// The reviewer's turn has ended by then, so the pool may close it.
	e.endReviewerTurn(reviewer)
	e.tick()
	if e.reviewers[reviewer].Status != "closed" || len(e.launches) != 1 {
		t.Fatal("draft retained its live reviewer")
	}
}

func TestReviewQueuePrioritizesPassingCIOverAge(t *testing.T) {
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	st := State{Records: map[string]*Record{}}
	for _, item := range []struct {
		key, ci string
		age     time.Duration
	}{
		{"old-pending", "pending", -48 * time.Hour},
		{"old-absent", "absent", -24 * time.Hour},
		{"green-z", "passed", -time.Hour},
		{"green-a", "passed", 0},
	} {
		st.Records[item.key] = &Record{Key: item.key, Head: "head", Watching: true, State: "open", RoomID: "room", CI: item.ci, Created: now.Add(item.age)}
	}
	s := &Service{}
	facts, err := s.ReviewFacts(st)
	if err != nil {
		t.Fatal(err)
	}
	queue := facts.Queue("room")
	want := []string{"green-a", "green-z", "old-absent", "old-pending"}
	if len(queue) != len(want) {
		t.Fatalf("queue = %+v", queue)
	}
	for i, key := range want {
		if queue[i].Key != key {
			t.Fatalf("queue[%d] = %s, want %s", i, queue[i].Key, key)
		}
	}
}

func TestDraftRemoteCheckPreventsReviewerLaunch(t *testing.T) {
	e := newEnv(t)
	e.setCI("success", 1)
	e.write()
	e.track()
	if err := e.s.Configure(e.author.RoomID, Reviewer{Profile: "judge"}); err != nil {
		t.Fatal(err)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.s.refreshHeads(context.Background(), st, e.clock, nil); err != nil {
		t.Fatal(err)
	}
	if r := e.record(); r.IsDraft || r.CI == "failed" {
		t.Fatalf("setup was not ready for review: %+v", r)
	}
	// GitHub changes to draft after the batch read, before the dispatch recheck.
	e.f.Draft = true
	e.write()
	e.clock = e.clock.Add(time.Minute)
	if err = e.s.dispatchAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.launches) != 0 || e.record().Reviewer != "" {
		t.Fatal("a fresh draft observation started a reviewer")
	}
}

func TestDraftTransitionDuringReviewLaunchRefusesAssignment(t *testing.T) {
	e := newEnv(t)
	e.setCI("success", 1)
	r := e.track()
	launch := e.s.LaunchReviewer
	e.s.LaunchReviewer = func(ctx context.Context, r Record, name, profile, task string) (string, error) {
		if err := e.s.Store.update(func(st *State) error { st.Records[r.Key].IsDraft = true; return nil }); err != nil {
			return "", err
		}
		return launch(ctx, r, name, profile, task)
	}
	e.tick()
	if len(e.launches) != 1 || e.record().Reviewer != "" {
		t.Fatalf("draft acquired an assignment: %+v", e.record())
	}
	for _, info := range e.reviewers {
		if info.Status != "closed" {
			t.Fatalf("unassigned reviewer stayed live: %+v", info)
		}
	}
	if e.record().Head != r.Head {
		t.Fatal("test changed the head")
	}
}
