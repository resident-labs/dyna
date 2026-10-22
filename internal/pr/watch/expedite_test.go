package watch

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/profile"
)

func setTestExpedite(t *testing.T, s *Service, room string, until time.Time) {
	t.Helper()
	if err := s.queueGoalStore().Update(func(st *goals.State) error {
		st.Room(room).Goals["g1"] = &goals.Goal{ID: "g1", Room: room, State: "active", ExpediteUntil: &until}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestExpeditedReviewGrantsKeepRunningWorkAndServeOldestNormal(t *testing.T) {
	e, keys := readyReviewPool(t, 6)
	e.s.ReviewerProfiles = func() ([]profile.Profile, error) { return []profile.Profile{{Name: "judge", MaxConcurrent: 1}}, nil }
	var granted []string
	launch := e.s.LaunchReviewer
	e.s.LaunchReviewer = func(ctx context.Context, r Record, name, profile, task string) (string, error) {
		granted = append(granted, r.Key)
		return launch(ctx, r, name, profile, task)
	}
	dispatchReviewPool(t, e)
	setTestExpedite(t, e.s, e.author.RoomID, e.clock.Add(24*time.Hour))
	if err := e.s.Store.update(func(st *State) error {
		for _, key := range keys[2:] {
			st.Records[key].GoalID = "g1"
		}
		st.Records[keys[1]].CI = "pending"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	dispatchReviewPool(t, e)
	if !reflect.DeepEqual(granted, keys[:1]) {
		t.Fatalf("running review was interrupted: %v", granted)
	}
	want := []string{keys[0], keys[2], keys[3], keys[4], keys[1], keys[5]}
	for i := 1; i < len(want); i++ {
		if err := e.s.Store.update(func(st *State) error { st.Records[want[i-1]].Verdict = "approve"; return nil }); err != nil {
			t.Fatal(err)
		}
		status, err := e.s.ReviewerStatus(e.author.RoomID)
		if err != nil {
			t.Fatal(err)
		}
		if len(status.Queue) == 0 || status.Queue[0].ChangeID != want[i] || status.ExpeditedGoalID != "g1" {
			t.Fatalf("next queue at %d: %+v", i, status)
		}
		dispatchReviewPool(t, e)
		if len(granted) != i+1 || granted[i] != want[i] {
			t.Fatalf("grants = %v; want prefix of %v", granted, want)
		}
	}
}

func TestReviewPriorityExpiresWithoutAWrite(t *testing.T) {
	e, keys := readyReviewPool(t, 2)
	until := e.clock.Add(time.Hour)
	setTestExpedite(t, e.s, e.author.RoomID, until)
	if err := e.s.Store.update(func(st *State) error { st.Records[keys[1]].GoalID = "g1"; return nil }); err != nil {
		t.Fatal(err)
	}
	before, err := e.s.ReviewerStatus(e.author.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Queue[0].ChangeID != keys[1] || !before.Queue[0].Expedited {
		t.Fatalf("not expedited: %+v", before.Queue)
	}
	e.clock = until
	after, err := e.s.ReviewerStatus(e.author.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Queue[0].ChangeID != keys[0] || after.ExpeditedGoalID != "" {
		t.Fatalf("expired priority remains: %+v", after)
	}
}

func TestExpeditedMergeTrialsAreIsolatedAndFair(t *testing.T) {
	f := mergeTrialEnv(t, 1, 2, 3, 4, 5, 6)
	s := f.e.s
	setTestExpedite(t, s, f.e.author.RoomID, f.e.clock.Add(24*time.Hour))
	if err := s.Store.update(func(st *State) error {
		for _, r := range st.Records {
			if r.Number >= 3 {
				r.GoalID = "g1"
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := [][]int{{3}, {4}, {5}, {1, 2}, {6}}
	for i, numbers := range want {
		f.head = fmt.Sprintf("%040x", 1000+i)
		// Simulate the PR watcher confirming each fresh CI probe between trials.
		if err := s.Store.update(func(st *State) error {
			for _, r := range st.Records {
				if r.State == "open" {
					r.CI = "passed"
					r.Polling.CIVersion = "SUCCESS"
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.MergeTick(t.Context()); err != nil {
			t.Fatal(err)
		}
		st, err := s.Store.Read()
		if err != nil {
			t.Fatal(err)
		}
		if len(st.MergeTrials) != 1 {
			for _, r := range st.Records {
				t.Logf("PR %d CI=%s reason=%s checks=%+v", r.Number, r.CI, r.CIReason, r.Checks)
			}
			t.Fatalf("trial %d: %+v queue=%+v merged=%v", i, st.MergeTrials, mergeQueue(st), *f.merged)
		}
		var got []int
		for _, trial := range st.MergeTrials {
			for _, c := range trial.Changes {
				got = append(got, st.Records[c.Key].Number)
			}
		}
		if !reflect.DeepEqual(got, numbers) {
			t.Fatalf("trial %d = %v, want %v", i, got, numbers)
		}
		// A repeated tick observes running CI and must retain the same batch.
		if _, err := s.MergeTick(t.Context()); err != nil {
			t.Fatal(err)
		}
		after, err := s.Store.Read()
		if err != nil {
			t.Fatal(err)
		}
		for repo, trial := range st.MergeTrials {
			live := after.MergeTrials[repo]
			if live == nil || trial.Branch != live.Branch || !reflect.DeepEqual(trial.Changes, live.Changes) {
				t.Fatal("running trial membership changed")
			}
		}
		f.ci = "completed"
		if _, err := s.MergeTick(t.Context()); err != nil {
			t.Fatal(err)
		}
		f.ci = "in_progress"
	}
}
