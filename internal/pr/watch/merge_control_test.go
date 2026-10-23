package watch

import (
	"context"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestMergeControlPauseResumeAndStatus(t *testing.T) {
	f := mergeTrialEnv(t, 1)
	id := f.e.author.RoomID
	if err := f.e.s.MergeControl(t.Context(), id, "pause", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	status, err := f.e.s.MergeControlStatus(id)
	if err != nil || !status.Paused || status.Reason != "merging paused by owner" || len(status.Queue) != 1 || f.builds != 0 {
		t.Fatalf("status=%+v error=%v builds=%d", status, err, f.builds)
	}
	if err := f.e.s.MergeControl(t.Context(), id, "resume", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.builds != 1 {
		t.Fatal("resume did not restart batching")
	}
}

func TestMergeControlCancelReturnsBatchToQueue(t *testing.T) {
	f := mergeTrialEnv(t, 1, 2)
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := f.e.s.MergeControl(t.Context(), f.e.author.RoomID, "cancel", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	st, err := f.e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.branches) != 0 || len(st.MergeTrials) != 0 {
		t.Fatal("cancel retained trial branches")
	}
	for _, r := range st.Records {
		if r.MergeTrialFailure != nil {
			t.Fatal("cancel returned a PR to its author")
		}
	}
}

func TestMergeControlReturnHoldsOnlyExactHead(t *testing.T) {
	f := mergeTrialEnv(t, 1, 2)
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := f.e.s.MergeControl(t.Context(), f.e.author.RoomID, "return", []string{"2", "Fix TestBroken; https://github.com/owner/repo/actions/runs/81"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	st, err := f.e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	r := st.Records["github.com/owner/repo#2"]
	if r.MergeTrialFailure == nil || r.MergeTrialFailure.Change.Head != r.Head || len(f.branches) != 0 {
		t.Fatal("return failed to hold frozen head or discard its active batch")
	}
	if returned := mergeNotices(f.e.sent, room.MergeReturned); len(returned) != 1 || returned[0].Target != r.Author || !strings.Contains(returned[0].Text, "TestBroken") {
		t.Fatalf("author notices=%+v", f.e.sent)
	}
	// The event carries the whole batch, so it names the returned member: the
	// card must not claim that the other member was returned too.
	if returned := mergeNotices(f.e.sent, room.MergeReturned); len(returned) == 1 {
		ev := returned[0].Merge
		if ev == nil || ev.Member != 2 || len(ev.PRs) != 2 {
			t.Fatalf("returned event = %+v, want member 2 of two", ev)
		}
	}
	rows, err := f.e.s.RoomMergeQueue(f.e.author.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ChangeID == r.ChangeID && row.State != "trial-rejected" {
			t.Fatalf("returned PR rejoined: %+v", row)
		}
	}
}

func TestMergeControlRetryWithoutStartsOneSharedBatch(t *testing.T) {
	f := mergeTrialEnv(t, 1, 2, 3)
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := f.e.s.MergeControl(t.Context(), f.e.author.RoomID, "pause", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.e.s.MergeControl(t.Context(), f.e.author.RoomID, "retry", nil, []string{"2"}, nil); err != nil {
		t.Fatal(err)
	}
	st, _ := f.e.s.Store.Read()
	b := st.MergeTrials["github.com/owner/repo"]
	if b == nil || len(b.Changes) != 2 || st.MergePauses[f.e.author.RoomID] != "" {
		t.Fatalf("retry=%+v pauses=%v", b, st.MergePauses)
	}
	for _, c := range b.Changes {
		if strings.HasSuffix(c.Key, "#2") {
			t.Fatal("excluded PR in retry")
		}
	}
}

func TestMergeControlRetryCancellationKeepsReplacementResumable(t *testing.T) {
	f := mergeTrialEnv(t, 1)
	id := f.e.author.RoomID
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := f.e.s.MergeControl(t.Context(), id, "pause", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	before, err := f.e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	oldBranch := before.MergeTrials["github.com/owner/repo"].Branch
	run := f.e.s.GH.run
	f.e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		if filepath.Base(cmd.Path) == "git" && len(cmd.Args) > 1 && cmd.Args[1] == "checkout" {
			return context.Canceled
		}
		return run(ctx, cmd)
	}
	if err := f.e.s.MergeControl(t.Context(), id, "retry", nil, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("retry error = %v, want cancellation", err)
	}
	// Read durable state as the restarted engine would.
	after, err := f.e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	batch := after.MergeTrials["github.com/owner/repo"]
	if batch == nil || batch.Branch == oldBranch || batch.Phase != "building" || after.MergePauses[id] != "" {
		t.Fatalf("replacement = %+v, pauses = %v", batch, after.MergePauses)
	}
	f.e.s.GH.run = run
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err = f.e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if batch := after.MergeTrials["github.com/owner/repo"]; batch == nil || batch.Phase != "running" {
		t.Fatalf("replacement did not resume: %+v", batch)
	}
}

func TestMergeControlBisectIsExplicitAndBounded(t *testing.T) {
	f := mergeTrialEnv(t, 1, 2, 3, 4)
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.ci, f.conclusion = "completed", "failure"
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.builds != 1 {
		t.Fatal("automatic prefix trial")
	}
	if err := f.e.s.MergeControl(t.Context(), f.e.author.RoomID, "bisect", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.builds != 4 {
		t.Fatalf("prefix builds=%d", f.builds)
	}
}

func TestMergeControlPublishRequiresPassedTrial(t *testing.T) {
	for _, ci := range []string{"pending", "success", "failure"} {
		t.Run(ci, func(t *testing.T) {
			f := mergeTrialEnv(t, 1, 2)
			if _, err := f.e.s.MergeTick(t.Context()); err != nil {
				t.Fatal(err)
			}
			if ci != "pending" {
				f.ci = "completed"
				f.conclusion = ci
			}
			err := f.e.s.MergeControl(t.Context(), f.e.author.RoomID, "publish", []string{"1"}, nil, nil)
			if ci == "success" {
				if err != nil || len(*f.merged) != 2 {
					t.Fatalf("publish=%v merged=%v", err, *f.merged)
				}
			} else if err == nil || len(*f.merged) != 0 {
				t.Fatalf("published without green push CI: %v", err)
			}
		})
	}
}

func TestMergeControlRefusesWorkerAndReadOnlyRoom(t *testing.T) {
	f := mergeTrialEnv(t, 1)
	t.Setenv("DYNA_MODE", "subagent")
	t.Setenv("DYNA_SESSION", "ses_test")
	if err := f.e.s.MergeControl(context.Background(), f.e.author.RoomID, "pause", nil, nil, nil); err == nil {
		t.Fatal("worker can control merging")
	}
}

func TestMergeControlFailureCancelResumeBuildsNewTrial(t *testing.T) {
	f := mergeTrialEnv(t, 1, 2)
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.ci, f.conclusion = "completed", "failure"
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	id := f.e.author.RoomID
	if err := f.e.s.MergeControl(t.Context(), id, "cancel", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.e.s.MergeControl(t.Context(), id, "resume", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	// Root may request a fresh batch without waiting for the next watch tick.
	if err := f.e.s.MergeControl(t.Context(), id, "retry", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.builds != 2 {
		t.Fatalf("recovery did not start a new trial: %d", f.builds)
	}
}

func TestMergeControlPublishPreservesPauseAndRejectsNonAncestor(t *testing.T) {
	for _, ancestor := range []bool{false, true} {
		t.Run(strconv.FormatBool(ancestor), func(t *testing.T) {
			f := mergeTrialEnv(t, 1, 2)
			if _, err := f.e.s.MergeTick(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := f.e.s.MergeControl(t.Context(), f.e.author.RoomID, "pause", nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			f.ci = "completed"
			run := f.e.s.GH.run
			f.e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
				if !ancestor && filepath.Base(cmd.Path) == "git" && len(cmd.Args) > 1 && cmd.Args[1] == "merge-base" {
					return errors.New("not an ancestor")
				}
				return run(ctx, cmd)
			}
			err := f.e.s.MergeControl(t.Context(), f.e.author.RoomID, "publish", []string{"1"}, nil, nil)
			if ancestor {
				if err != nil || len(*f.merged) != 2 {
					t.Fatalf("publish=%v merged=%v", err, *f.merged)
				}
			} else if err == nil || len(*f.merged) > 0 {
				t.Fatalf("non-ancestor published: %v", err)
			}
			status, err := f.e.s.MergeControlStatus(f.e.author.RoomID)
			if err != nil || !status.Paused {
				t.Fatal("explicit publication cleared the room pause")
			}
		})
	}
}

func TestMergeControlRejectsReadOnlyAndOtherRootRoom(t *testing.T) {
	f := mergeTrialEnv(t, 1)
	id := f.e.author.RoomID
	if err := ghclient.SetAccess(f.e.s.Notify.Rooms, id, ghclient.ReadOnly); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"pause", "resume", "cancel", "retry", "bisect"} {
		if err := f.e.s.MergeControl(t.Context(), id, action, nil, nil, nil); err == nil {
			t.Fatalf("read-only action %s accepted", action)
		}
	}
	if err := ghclient.SetAccess(f.e.s.Notify.Rooms, id, ghclient.ReadWrite); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DYNA_MODE", "root")
	t.Setenv("DYNA_ROOM_ID", "different-room")
	if err := f.e.s.MergeControl(t.Context(), id, "pause", nil, nil, nil); err == nil {
		t.Fatal("root changed another room")
	}
}

func TestMergeControlRetryWithoutAllPRsPreservesPause(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(strconv.FormatBool(paused), func(t *testing.T) {
			f := mergeTrialEnv(t, 1)
			if paused {
				if err := f.e.s.MergeControl(t.Context(), f.e.author.RoomID, "pause", nil, nil, nil); err != nil {
					t.Fatal(err)
				}
			}
			err := f.e.s.MergeControl(t.Context(), f.e.author.RoomID, "retry", nil, []string{"1"}, nil)
			if err == nil || f.builds != 0 {
				t.Fatal("empty retry was accepted")
			}
			status, err := f.e.s.MergeControlStatus(f.e.author.RoomID)
			if err != nil || status.Paused != paused {
				t.Fatalf("empty retry changed pause: %+v, error: %v", status, err)
			}
		})
	}
}

func TestMergeControlPublishRefusesPausedSessions(t *testing.T) {
	f := mergeTrialEnv(t, 1)
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.s.Notify.Rooms.SetSessionsPaused(t.Context(), f.e.author.RoomID, true); err != nil {
		t.Fatal(err)
	}
	f.ci = "completed"
	if err := f.e.s.MergeControl(t.Context(), f.e.author.RoomID, "publish", []string{"1"}, nil, nil); err == nil {
		t.Fatal("publish reported success while sessions were paused")
	}
}

func TestMergeControlPublishesChosenPrefixWithoutReturningAnotherPR(t *testing.T) {
	f := mergeTrialEnv(t, 1, 2, 3)
	f.uniqueHeads = true
	f.trialCI = map[int]string{3: "failure", 1: "pending", 2: "success"}
	for range 2 {
		if _, err := f.e.s.MergeTick(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.e.s.MergeControl(t.Context(), f.e.author.RoomID, "bisect", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := f.e.s.MergeControl(t.Context(), f.e.author.RoomID, "pause", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.e.s.Store.update(func(st *State) error {
		b := st.MergeTrials["github.com/owner/repo"]
		b.Failure = &state.MergeTrialFailure{Change: b.Changes[2], Reason: "old automatic choice"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.e.s.MergeControl(t.Context(), f.e.author.RoomID, "publish", []string{"3"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*f.merged, []int{1, 2}) {
		t.Fatalf("wrong prefix: %v", *f.merged)
	}
	st, err := f.e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if st.Records["github.com/owner/repo#3"].MergeTrialFailure != nil {
		t.Fatal("manual publication returned another PR without root choice")
	}
	if st.MergePauses[f.e.author.RoomID] == "" || len(f.branches) > 0 {
		t.Fatal("manual prefix publication lost pause or trial cleanup")
	}
}

func TestMergeControlRepeatedPublishReportsAlreadyPublished(t *testing.T) {
	for _, name := range []string{"1", "dyna/merge-trial/1"} {
		t.Run(name, func(t *testing.T) {
			f := mergeTrialEnv(t, 1)
			if _, err := f.e.s.MergeTick(t.Context()); err != nil {
				t.Fatal(err)
			}
			release := func() {}
			t.Cleanup(func() { release() })
			run := f.e.s.GH.run
			f.e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
				args := cmd.Args[1:]
				if filepath.Base(cmd.Path) == "git" && len(args) > 0 && args[0] == "push" && strings.HasSuffix(args[len(args)-1], ":refs/heads/main") {
					unlock, owned, err := f.e.s.tryPublicationGate("github.com/owner/repo#1")
					if err != nil || !owned {
						return fmt.Errorf("publication gate owned=%v: %w", owned, err)
					}
					release = sync.OnceFunc(unlock)
				}
				return run(ctx, cmd)
			}
			f.ci = "completed"
			if err := f.e.s.MergeControl(t.Context(), f.e.author.RoomID, "publish", []string{name}, nil, nil); err != nil {
				t.Fatal(err)
			}
			st, err := f.e.s.Store.Read()
			if err != nil {
				t.Fatal(err)
			}
			if b := st.MergeTrials["github.com/owner/repo"]; b == nil || b.Phase != "published" {
				t.Fatalf("published checkpoint lost: %+v", b)
			}
			// New main CI may still be pending, and trial refs can be cleaned later.
			f.ci = "in_progress"
			calls := 0
			f.e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error { calls++; return run(ctx, cmd) }
			err = f.e.s.MergeControl(t.Context(), f.e.author.RoomID, "publish", []string{name}, nil, nil)
			if err == nil || !strings.Contains(err.Error(), "already published") {
				t.Fatalf("repeat publish result: %v", err)
			}
			if calls != 0 || len(*f.merged) != 1 {
				t.Fatalf("repeat publish made %d calls and merged %v", calls, *f.merged)
			}
		})
	}
}

// Returning the only PR of a failed batch leaves no batch to recover from, so
// the batch-failure pause must not keep the queue stopped until root resumes it
// by hand.
func TestReturnClearsThePauseWhenNoFailedBatchRemains(t *testing.T) {
	f := mergeTrialEnv(t, 1)
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.ci, f.conclusion = "completed", "failure"
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, err := f.e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if st.MergePauses[f.e.author.RoomID] != mergeBatchFailedReason {
		t.Fatalf("pauses = %v, want the failed-batch pause", st.MergePauses)
	}
	if err := f.e.s.MergeControl(t.Context(), f.e.author.RoomID, "return", []string{"1", "Fix the go-static error"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	st, err = f.e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.MergeTrials) != 0 || st.MergePauses[f.e.author.RoomID] != "" {
		t.Fatalf("trials=%d pauses=%v; a return left the queue paused with nothing to recover", len(st.MergeTrials), st.MergePauses)
	}
}
