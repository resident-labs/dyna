package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

type trialFixture struct {
	e                                               *testEnv
	fs                                              map[int]*fixture
	merged                                          *[]int
	main, head, branch, ci                          string
	event, observedHead, observedBranch, conclusion string
	conflictHead                                    string
	heads                                           []string
	branches                                        map[string]string
	builds                                          int
	uniqueHeads                                     bool
	commitHeads                                     map[string][]string
	trialCI                                         map[int]string
	jobs                                            []budgetJob
	// checks are the hosted checks of the trial head, as a failed job leaves
	// them while the rest of its run is still going.
	checks []Check
	// extraRuns are the trial head's other workflows beside the first one.
	extraRuns []Run
}

func mergeTrialEnv(t *testing.T, numbers ...int) *trialFixture {
	t.Helper()
	e, fs, merged := mergeEnv(t, numbers...)
	if err := ghclient.SetAccess(e.s.Notify.Rooms, e.author.RoomID, ghclient.ReadWrite); err != nil {
		t.Fatal(err)
	}
	f := &trialFixture{e: e, fs: fs, merged: merged, main: strings.Repeat("d", 40), head: strings.Repeat("e", 40), ci: "in_progress", branches: map[string]string{}}
	run := e.s.GH.run
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		args := cmd.Args[1:]
		joined := strings.Join(args, " ")
		if filepath.Base(cmd.Path) == "git" {
			switch args[0] {
			case "checkout":
				f.heads = nil
				f.builds++
			case "-c":
				if args[len(args)-1] == f.conflictHead {
					return errors.New("merge conflict")
				}
				f.heads = append(f.heads, args[len(args)-1])
			case "ls-files":
				if f.conflictHead != "" {
					fmt.Fprint(cmd.Stdout, "unmerged file")
				}
			case "rev-parse":
				sha := f.head
				if f.uniqueHeads {
					sha = fmt.Sprintf("%040x", 10000+len(f.heads))
				}
				if f.commitHeads == nil {
					f.commitHeads = map[string][]string{}
				}
				f.commitHeads[sha] = append([]string(nil), f.heads...)
				fmt.Fprint(cmd.Stdout, sha)
			case "ls-remote":
				ref := args[len(args)-1]
				head := f.branches[strings.TrimPrefix(ref, "refs/heads/")]
				if ref == "refs/heads/main" {
					head = f.main
				}
				if head != "" {
					fmt.Fprintf(cmd.Stdout, "%s\t%s\n", head, ref)
				}
			case "push":
				head, ref, _ := strings.Cut(args[len(args)-1], ":refs/heads/")
				if ref == "main" {
					f.main = head
					for _, sha := range f.commitHeads[head] {
						for n, pull := range fs {
							if pull.Pull.Head.SHA == sha {
								*merged = append(*merged, n)
								pull.Pull.State = "closed"
								pull.Pull.Merged = true
							}
						}
					}
				} else {
					f.branch = ref
					if head == "" {
						delete(f.branches, ref)
					} else {
						f.branches[ref] = head
					}
				}
			}
			return nil
		}
		if strings.Contains(joined, "/actions/runs/81/attempts/1/jobs?") {
			fmt.Fprint(cmd.Stdout, "HTTP/2.0 200 OK\r\n\r\n")
			return json.NewEncoder(cmd.Stdout).Encode(map[string]any{"jobs": f.jobs})
		}
		trialHead := f.head
		for sha := range f.commitHeads {
			if strings.Contains(joined, "/commits/"+sha+"/") || strings.Contains(joined, "actions/runs?head_sha="+sha) {
				trialHead = sha
				break
			}
		}
		if strings.Contains(joined, "/commits/"+trialHead+"/") || strings.Contains(joined, "actions/runs?head_sha="+trialHead) {
			fmt.Fprint(cmd.Stdout, "HTTP/2.0 200 OK\r\n\r\n")
			if strings.Contains(joined, "actions/runs?") {
				conclusion := ""
				if f.ci == "completed" {
					conclusion = "success"
				}
				event, head, branch := "push", trialHead, f.branch
				for name, sha := range f.branches {
					if sha == head {
						branch = name
						break
					}
				}
				status := f.ci
				if value := f.trialCI[len(f.commitHeads[head])]; value != "" {
					if value == "pending" {
						status = "in_progress"
						conclusion = ""
					} else {
						status = "completed"
						conclusion = value
					}
				}
				if f.event != "" {
					event = f.event
				}
				if f.observedHead != "" {
					head = f.observedHead
				}
				if f.observedBranch != "" {
					branch = f.observedBranch
				}
				if f.conclusion != "" {
					conclusion = f.conclusion
				}
				// extraRuns are further workflows of the same trial head, such as a
				// cancelled one beside the failed one.
				runs := append([]Run{{ID: 81, Attempt: 1, Event: event, HeadBranch: branch, HeadSHA: head, Status: status, Conclusion: conclusion}}, f.extraRuns...)
				return json.NewEncoder(cmd.Stdout).Encode(workflowRunsResponse{WorkflowRuns: runs})
			}
			if strings.Contains(joined, "check-runs?") {
				return json.NewEncoder(cmd.Stdout).Encode(checkRunsResponse{CheckRuns: f.checks})
			}
			return json.NewEncoder(cmd.Stdout).Encode([]Status{})
		}
		if strings.Contains(joined, "graphql") {
			var body bytes.Buffer
			out := cmd.Stdout
			cmd.Stdout = &body
			if err := run(ctx, cmd); err != nil {
				return err
			}
			var data map[string]any
			if err := json.Unmarshal([]byte(strings.SplitN(body.String(), "\r\n\r\n", 2)[1]), &data); err != nil {
				return err
			}
			target := data["data"].(map[string]any)["r0"].(map[string]any)["defaultBranchRef"].(map[string]any)["target"].(map[string]any)
			target["oid"] = f.main
			target["workflows"] = map[string]any{"object": map[string]any{"entries": []any{map[string]any{"name": "ci.yml", "type": "blob", "object": map[string]any{"text": "on: [push]\n", "isTruncated": false}}}}}
			fmt.Fprint(out, "HTTP/2.0 200 OK\r\n\r\n")
			return json.NewEncoder(out).Encode(data)
		}
		return run(ctx, cmd)
	}
	return f
}

func TestAutomaticMergeWaitsForExactBatchTrial(t *testing.T) {
	f := mergeTrialEnv(t, 3, 2, 1)
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(*f.merged) != 0 {
		t.Fatalf("published without a passing batch trial: %v", *f.merged)
	}
	if f.branch != "dyna/merge-trial/1" {
		t.Fatalf("trial branch=%q", f.branch)
	}
	f.ci = "completed"
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*f.merged, []int{3, 2, 1}) {
		t.Fatalf("published order=%v", *f.merged)
	}
	if f.main != f.head || len(f.branches) != 0 {
		t.Fatalf("main=%s trial branches=%v", f.main, f.branches)
	}
}

func TestBatchTrialResumesAfterRestart(t *testing.T) {
	f := mergeTrialEnv(t, 1)
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	restarted := *f.e.s
	restarted.Store = &Store{Root: f.e.s.Store.Root}
	f.ci = "completed"
	if _, err := restarted.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*f.merged, []int{1}) || f.builds != 1 {
		t.Fatalf("restart rebuilt or lost batch: merged=%v builds=%d", *f.merged, f.builds)
	}
}

func TestBatchTrialRequiresExactPushEvidence(t *testing.T) {
	for _, which := range []string{"head", "branch", "event"} {
		t.Run(which, func(t *testing.T) {
			f := mergeTrialEnv(t, 1)
			if _, err := f.e.s.MergeTick(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.ci = "completed"
			switch which {
			case "head":
				f.observedHead = strings.Repeat("f", 40)
			case "branch":
				f.observedBranch = "main"
			case "event":
				f.event = "pull_request"
			}
			if _, err := f.e.s.MergeTick(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(*f.merged) != 0 {
				t.Fatal("published without CI for the exact trial")
			}
		})
	}
}

func TestBatchTrialRechecksPRBeforePublication(t *testing.T) {
	for _, which := range []string{"head", "ci", "approval", "draft", "policy", "main"} {
		t.Run(which, func(t *testing.T) {
			f := mergeTrialEnv(t, 1)
			if _, err := f.e.s.MergeTick(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.ci = "completed"
			switch which {
			case "head":
				f.fs[1].Pull.Head.SHA = strings.Repeat("f", 40)
			case "ci":
				f.fs[1].Checks[0].Conclusion = "failure"
			case "draft":
				f.fs[1].Draft = true
			case "main":
				f.main = strings.Repeat("f", 40)
			case "approval":
				if err := f.e.s.Store.update(func(st *State) error { st.Records["github.com/owner/repo#1"].Verdict = "changes"; return nil }); err != nil {
					t.Fatal(err)
				}
			case "policy":
				f.fs[1].Checks = append(f.fs[1].Checks, Check{Name: "external", Status: "completed", Conclusion: "failure"})
				if err := f.e.s.Store.update(func(st *State) error {
					st.Records["github.com/owner/repo#1"].PolicyOverride.RequiredChecks = []string{"external"}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.e.s.MergeTick(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(*f.merged) != 0 || len(f.branches) != 0 {
				t.Fatalf("published or kept invalid trial: merged=%v branches=%v", *f.merged, f.branches)
			}
		})
	}
}

func TestBatchTrialNeverPublishesInReadOnlyOrPausedRoom(t *testing.T) {
	for _, which := range []string{"read-only", "paused"} {
		for _, during := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/during=%v", which, during), func(t *testing.T) {
				f := mergeTrialEnv(t, 1)
				if during {
					if _, err := f.e.s.MergeTick(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				f.ci = "completed"
				if which == "read-only" {
					if err := ghclient.SetAccess(f.e.s.Notify.Rooms, f.e.author.RoomID, ghclient.ReadOnly); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err := f.e.s.Notify.Rooms.SetSessionsPaused(t.Context(), f.e.author.RoomID, true); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := f.e.s.MergeTick(t.Context()); err != nil {
					t.Fatal(err)
				}
				if len(*f.merged) != 0 {
					t.Fatal("held room published a trial")
				}
				if !during && f.builds != 0 {
					t.Fatal("held room built a trial")
				}
				rows, err := f.e.s.RoomMergeQueue(f.e.author.RoomID)
				if err != nil || len(rows) != 1 || rows[0].State != which {
					t.Fatalf("hold status=%+v error=%v", rows, err)
				}
			})
		}
	}
}

func TestBatchTrialExcludesConflictAndNotifiesAuthor(t *testing.T) {
	f := mergeTrialEnv(t, 1, 2, 3)
	f.conflictHead = f.fs[2].Pull.Head.SHA
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.ci = "completed"
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*f.merged, []int{1, 3}) {
		t.Fatalf("merged=%v", *f.merged)
	}
	found := false
	for _, n := range f.e.sent {
		if n.Target == f.e.author.ID && strings.Contains(n.Text, "/pull/2 conflicts") {
			found = true
			// The event names the conflicting member, so its card does not
			// claim that the PRs which did merge are in conflict too.
			if n.Merge == nil || n.Merge.Member != 2 {
				t.Fatalf("conflict event = %+v, want member 2", n.Merge)
			}
		}
	}
	if !found {
		t.Fatal("conflicting PR author was not notified")
	}
}

func TestBatchTrialFailureLeavesMainUntouched(t *testing.T) {
	f := mergeTrialEnv(t, 1, 2)
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
	b := st.MergeTrials["github.com/owner/repo"]
	if b == nil || b.Phase != "awaiting-root" || b.Logs == "" || len(*f.merged) != 0 {
		t.Fatalf("failure state=%+v merged=%v", b, *f.merged)
	}
}

func TestBatchTrialKeepsStateUntilMergeIsRecorded(t *testing.T) {
	f := mergeTrialEnv(t, 1)
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	release := func() {}
	defer func() { release() }()
	run := f.e.s.GH.run
	f.e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		args := cmd.Args[1:]
		if filepath.Base(cmd.Path) == "git" && args[0] == "push" && strings.HasSuffix(args[len(args)-1], ":refs/heads/main") {
			unlock, owned, err := f.e.s.tryPublicationGate("github.com/owner/repo#1")
			if err != nil || !owned {
				return fmt.Errorf("publication gate: owned=%v error=%v", owned, err)
			}
			release = sync.OnceFunc(unlock)
		}
		return run(ctx, cmd)
	}
	f.ci = "completed"
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, err := f.e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if b := st.MergeTrials["github.com/owner/repo"]; b == nil || b.Phase != "published" {
		t.Fatal("forgot a merge before its record was durable")
	}
	release()
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, err = f.e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.MergeTrials) != 0 || !reflect.DeepEqual(*f.merged, []int{1}) {
		t.Fatal("recovery repeated or lost publication")
	}
}

func TestFailedBatchPausesAndNotifiesRootWithoutPrefixTrials(t *testing.T) {
	f := mergeTrialEnv(t, 1, 2, 3, 4, 5)
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.ci, f.conclusion = "completed", "failure"
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(f.branches) != 1 || f.builds != 1 {
		t.Fatalf("failed batch started automatic prefixes: branches=%v builds=%d", f.branches, f.builds)
	}
	st, err := f.e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got := st.MergeTrials["github.com/owner/repo"]; got == nil || got.Phase != "awaiting-root" {
		t.Fatalf("failed batch did not wait for root: %+v", got)
	}
	why, err := f.e.s.trialRoomHold(t.Context(), *st.Records["github.com/owner/repo#1"])
	if err != nil || !strings.Contains(why, "merging paused") {
		t.Fatalf("room not paused: %q, %v", why, err)
	}
	f.e.s.Store = &Store{Root: f.e.s.Store.Root}
	for range 3 {
		if _, err := f.e.s.MergeTick(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	var notices []Notice
	for _, n := range f.e.sent {
		if strings.Contains(n.Text, "Batch trial") {
			notices = append(notices, n)
		}
	}
	if len(notices) != 1 || notices[0].Target != "" || notices[0].Kind == "informational" {
		t.Fatalf("want one waking root notice, got %+v", notices)
	}
	for _, key := range []string{"/pull/1", "/pull/2", "/pull/3", "/pull/4", "/pull/5"} {
		if !strings.Contains(notices[0].Text, key) {
			t.Fatalf("root notice lacks %s: %s", key, notices[0].Text)
		}
	}
	if len(*f.merged) != 0 {
		t.Fatal("failed batch changed main")
	}
}

func TestStackedTrialsPublishLongestPassingPrefixAndReturnFailure(t *testing.T) {
	f := mergeTrialEnv(t, 1, 2, 3, 4)
	f.uniqueHeads = true
	f.trialCI = map[int]string{4: "failure", 1: "success", 2: "success", 3: "failure"}
	for range 5 {
		if _, err := f.e.s.MergeTick(t.Context()); err != nil {
			t.Fatal(err)
		}
		requestFixtureBisect(t, f)
	}
	if !reflect.DeepEqual(*f.merged, []int{1, 2}) {
		t.Fatalf("merged=%v", *f.merged)
	}
	if len(f.branches) != 0 {
		t.Fatalf("leaked branches=%v", f.branches)
	}
	st, err := f.e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	rejected := st.Records["github.com/owner/repo#3"]
	if rejected.MergeTrialFailure == nil || rejected.MergeTrialFailure.Change.Head != rejected.Head {
		t.Fatal("first failing PR was not held")
	}
	found := false
	for _, notice := range f.e.sent {
		if strings.Contains(notice.Text, "/pull/3 failed merge trial CI") && strings.Contains(notice.Text, "CI") {
			found = true
			// The passing prefix published first, so the trial no longer lists
			// the failed member. The event must still carry it: the card names
			// that PR, and must not fall back to a member that merged.
			ev := notice.Merge
			if ev == nil || ev.Member != 3 {
				t.Fatalf("failed event = %+v, want member 3", ev)
			}
			named := false
			for _, pr := range ev.PRs {
				if pr.Number == 3 {
					named = true
				}
			}
			if !named {
				t.Fatalf("failed event PRs = %+v, want the failed member 3", ev.PRs)
			}
		}
	}
	if !found {
		t.Fatal("failed author did not get CI evidence")
	}
	rows, err := f.e.s.RoomMergeQueue(f.e.author.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ChangeID == rejected.ChangeID && row.State != "trial-rejected" {
			t.Fatalf("rejected row=%+v", row)
		}
	}
}

func TestStackedTrialsHoldPendingAndDiscardWhenMainMoves(t *testing.T) {
	f := mergeTrialEnv(t, 1, 2, 3)
	f.uniqueHeads = true
	f.trialCI = map[int]string{3: "failure", 1: "pending", 2: "success"}
	for range 4 {
		if _, err := f.e.s.MergeTick(t.Context()); err != nil {
			t.Fatal(err)
		}
		requestFixtureBisect(t, f)
	}
	if len(*f.merged) != 0 {
		t.Fatal("published before prefix results completed")
	}
	f.main = strings.Repeat("f", 40)
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(f.branches) != 0 {
		t.Fatal("kept stale stack branches")
	}
	st, err := f.e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range st.Records {
		if r.MergeTrialFailure != nil {
			t.Fatal("blamed author using stale main")
		}
	}
}

func TestSingleFailedTrialWaitsForRootWithoutReturningAuthor(t *testing.T) {
	f := mergeTrialEnv(t, 1)
	f.ci, f.conclusion = "completed", "failure"
	for range 5 {
		if _, err := f.e.s.MergeTick(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range f.e.sent {
		if n.Target != "" && strings.Contains(n.Text, "failed merge trial") {
			t.Fatal("returned a PR without root choice")
		}
	}
	if f.builds != 1 || len(f.branches) != 1 || len(*f.merged) != 0 {
		t.Fatalf("builds=%d branches=%v merged=%v", f.builds, f.branches, *f.merged)
	}
}

func TestStackedTrialsResumeAndMergeRemainingPRs(t *testing.T) {
	f := mergeTrialEnv(t, 1, 2, 3, 4)
	f.uniqueHeads = true
	f.trialCI = map[int]string{4: "failure", 1: "success", 2: "success", 3: "failure"}
	for range 3 {
		if _, err := f.e.s.MergeTick(t.Context()); err != nil {
			t.Fatal(err)
		}
		requestFixtureBisect(t, f)
	}
	before := f.builds
	restarted := *f.e.s
	restarted.Store = &Store{Root: f.e.s.Store.Root}
	if _, err := restarted.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.builds != before {
		t.Fatal("restart rebuilt completed prefixes")
	}
	// The rejected PR stays out, while the suffix can join a trial on new main.
	f.trialCI = map[int]string{1: "success"}

	// Refresh the suffix through the ordinary snapshot path with its observed
	// check version, as the poller does before the next merge pass.
	st, err := f.e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	member := *st.Records["github.com/owner/repo#4"]
	snap, err := restarted.github(member.RoomID).Snapshot(t.Context(), refRecord(member))
	if err != nil {
		t.Fatal(err)
	}
	snap.ciProbe = "SUCCESS"
	ci, key, jobs, _ := restarted.ciStatus(snap, member, EffectivePolicy(st, member))
	if err := restarted.process(t.Context(), member.Key, snap, ci, key, jobs, nil, false); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		if _, err := restarted.MergeTick(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(*f.merged, []int{1, 2, 4}) {
		st, _ := f.e.s.Store.Read()
		rows, _ := f.e.s.RoomMergeQueue(f.e.author.RoomID)
		t.Fatalf("merged=%v trials=%+v queue=%+v", *f.merged, st.MergeTrials, rows)
	}
}

func TestPublishingRetryRefreshesCombinedTrialCI(t *testing.T) {
	for _, conclusion := range []string{"pending", "failure"} {
		t.Run(conclusion, func(t *testing.T) {
			f := mergeTrialEnv(t, 1)
			if _, err := f.e.s.MergeTick(t.Context()); err != nil {
				t.Fatal(err)
			}
			run := f.e.s.GH.run
			fail := true
			f.e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
				if filepath.Base(cmd.Path) == "git" && cmd.Args[1] == "push" && strings.HasSuffix(cmd.Args[len(cmd.Args)-1], ":refs/heads/main") && fail {
					return errors.New("publication connection failed")
				}
				return run(ctx, cmd)
			}
			f.ci = "completed"
			if _, err := f.e.s.MergeTick(t.Context()); err == nil {
				t.Fatal("expected publication failure")
			}
			fail = false
			if conclusion == "pending" {
				f.ci = "in_progress"
			} else {
				f.conclusion = "failure"
			}
			if _, err := f.e.s.MergeTick(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(*f.merged) != 0 {
				t.Fatal("retried publication without current passing combined CI")
			}
		})
	}
}

func TestBatchTrialProgressUsesJobsWithoutAcceptingQueuedRun(t *testing.T) {
	for _, tc := range []struct {
		name, run, want string
		jobs            []budgetJob
	}{
		{"queued run has active jobs", "queued", "running (2 of 4 jobs done)", []budgetJob{{Status: "completed"}, {Status: "in_progress"}, {Status: "queued"}, {Status: "completed"}}},
		{"run started but jobs queued", "in_progress", "waiting for push CI to start", []budgetJob{{Status: "queued"}}},
		{"jobs done but run queued", "queued", "running (1 of 1 jobs done)", []budgetJob{{Status: "completed"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := mergeTrialEnv(t, 1)
			f.ci, f.jobs = tc.run, tc.jobs
			r, err := f.e.s.Store.Get("github.com/owner/repo#1")
			if err != nil {
				t.Fatal(err)
			}
			trial, err := f.e.s.mergeTrialCI(t.Context(), r, f.branch, f.head)
			if err != nil {
				t.Fatal(err)
			}
			if trial.Status != "pending" || trial.Reason != tc.want {
				t.Fatalf("CI=%q reason=%q, want pending and %q", trial.Status, trial.Reason, tc.want)
			}
		})
	}
}

func TestBadUnrelatedHeadProbeDoesNotBlockMergeTrial(t *testing.T) {
	for _, phase := range []string{"building", "publishing"} {
		t.Run(phase, func(t *testing.T) {
			f := mergeTrialEnv(t, 1, 2)
			f.fs[2].Draft = true
			if phase == "publishing" {
				if _, err := f.e.s.MergeTick(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			failTrialHeadProbe(f, 2)
			f.ci = "completed"
			// A failed probe can be reported, but unrelated work must advance.
			_, _ = f.e.s.MergeTick(t.Context())
			if phase == "building" {
				if f.builds != 1 {
					t.Fatalf("healthy trial did not build: %d", f.builds)
				}
				_, _ = f.e.s.MergeTick(t.Context())
			}
			if !reflect.DeepEqual(*f.merged, []int{1}) {
				t.Fatalf("healthy trial blocked by unrelated probe: %v", *f.merged)
			}
			bad, err := f.e.s.Store.Get("github.com/owner/repo#2")
			if err != nil {
				t.Fatal(err)
			}
			if !bad.HeadStale || bad.Error == "" {
				t.Fatalf("bad probe was not retained: %+v", bad)
			}
		})
	}
}

func failTrialHeadProbe(f *trialFixture, number int) {
	raw := f.e.s.GH.run
	f.e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		if !strings.Contains(strings.Join(cmd.Args, " "), "graphql") {
			return raw(ctx, cmd)
		}
		var body bytes.Buffer
		out := cmd.Stdout
		cmd.Stdout = &body
		if err := raw(ctx, cmd); err != nil {
			return err
		}
		var response map[string]any
		if err := json.Unmarshal(bytes.SplitN(body.Bytes(), []byte("\r\n\r\n"), 2)[1], &response); err != nil {
			return err
		}
		response["data"].(map[string]any)["r0"].(map[string]any)[fmt.Sprint("p", number)] = nil
		fmt.Fprint(out, "HTTP/2.0 200 OK\r\n\r\n")
		return json.NewEncoder(out).Encode(response)
	}
}

func TestFailedBatchMemberProbeKeepsTrialHeld(t *testing.T) {
	f := mergeTrialEnv(t, 1)
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	failTrialHeadProbe(f, 1)
	f.ci = "completed"
	_, err := f.e.s.MergeTick(t.Context())
	if err == nil || !strings.Contains(err.Error(), "github.com/owner/repo#1") {
		t.Fatalf("missing batch probe error: %v", err)
	}
	if len(*f.merged) != 0 {
		t.Fatal("unconfirmed batch member was published")
	}
	st, err := f.e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if batch := st.MergeTrials["github.com/owner/repo"]; batch == nil || batch.Phase != "running" {
		t.Fatalf("unconfirmed trial advanced: %+v", batch)
	}
}

func TestUntrackedTrialMemberDoesNotHoldOtherReadyPRs(t *testing.T) {
	f := mergeTrialEnv(t, 1, 2)
	f.fs[2].Draft = true
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := f.e.s.Store.update(func(st *State) error { st.Records["github.com/owner/repo#1"].Watching = false; return nil }); err != nil {
		t.Fatal(err)
	}
	f.fs[2].Draft = false
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, err := f.e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.MergeTrials) != 0 {
		t.Fatal("untracked member retained its trial")
	}
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, err = f.e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	b := st.MergeTrials["github.com/owner/repo"]
	if b == nil || len(b.Changes) != 1 || b.Changes[0].Key != "github.com/owner/repo#2" {
		t.Fatalf("ready PR did not advance: %+v", b)
	}
}

// Existing prefix tests request bisection through the same root API as the CLI.
// The normal worker never requests it after a failed batch.
func requestFixtureBisect(t *testing.T, f *trialFixture) {
	t.Helper()
	st, err := f.e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range st.MergeTrials {
		if b.Phase == "awaiting-root" {
			if err := f.e.s.MergeControl(t.Context(), f.e.author.RoomID, "bisect", nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
}

func TestFailedBatchNoticeRetriesWithoutRestartingTrials(t *testing.T) {
	f := mergeTrialEnv(t, 1, 2)
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.ci, f.conclusion = "completed", "failure"
	deliver := f.e.s.Deliver
	f.e.s.Deliver = func(context.Context, Record, Notice) (string, error) { return "", errors.New("room post unavailable") }
	if _, err := f.e.s.MergeTick(t.Context()); err == nil {
		t.Fatal("wanted report delivery error")
	}
	st, err := f.e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if st.MergePauses[f.e.author.RoomID] == "" || st.MergeTrials["github.com/owner/repo"].Phase != "awaiting-root" {
		t.Fatal("delivery failure lost the pause")
	}
	f.e.s.Deliver = deliver
	f.e.s.Store = &Store{Root: f.e.s.Store.Root}
	// A failed head probe must not prevent delivery of the frozen root report.
	run := f.e.s.GH.run
	f.e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		if strings.Contains(strings.Join(cmd.Args, " "), "graphql") {
			return errors.New("head probe unavailable")
		}
		return run(ctx, cmd)
	}
	if _, err := f.e.s.MergeTick(t.Context()); err == nil {
		t.Fatal("expected probe error")
	}
	if failed := mergeNotices(f.e.sent, room.MergeFailed); len(failed) != 1 || f.builds != 1 {
		t.Fatalf("notices=%v builds=%d", f.e.sent, f.builds)
	}
}

func TestTrialBatchNamesPRsInMergeCommits(t *testing.T) {
	head := strings.Repeat("a", 40)
	r := Record{Key: "github.com/o/r#7", Host: "github.com", Repo: "o/r", Number: 7, Title: "Fix parser", Head: head}
	batch := trialBatch(r, state.MergeTrial{Changes: []state.MergeTrialChange{trialChange(r)}})
	if got := batch.Messages[head]; got != "Merge #7: Fix parser\n\nPR head: "+head {
		t.Fatalf("trial merge message = %q", got)
	}
}

func TestReadyBatchStartsBuildDirectly(t *testing.T) {
	f := mergeTrialEnv(t, 1, 2)
	f.e.s.Notify.Sessions.Store = &subagent.Store{Root: t.TempDir()}
	r, err := f.e.s.Notify.Rooms.Create(room.CreateInput{Name: "dyna-agent", Repo: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := ghclient.SetAccess(f.e.s.Notify.Rooms, r.ID, ghclient.ReadWrite); err != nil {
		t.Fatal(err)
	}
	if err := f.e.s.Store.update(func(st *State) error {
		for _, rec := range st.Records {
			rec.RoomID = r.ID
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.e.s.StartAgent = func(subagent.StartOptions) (subagent.Info, error) {
		t.Fatal("ready batch started an agent before its build")
		return subagent.Info{}, nil
	}
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, err := f.e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	b := st.MergeTrials["github.com/owner/repo"]
	if b == nil || b.Phase != "running" || len(b.Changes) != 2 || f.builds != 1 {
		t.Fatalf("ready batch did not build directly: batch=%+v builds=%d", b, f.builds)
	}
}

func TestSavedReviewingBatchBuildsAndClosesOldSession(t *testing.T) {
	f := mergeTrialEnv(t, 1, 2)
	const repo = "github.com/owner/repo"
	const session = "ses_20261019-000001-aaaaaaaa"
	const branch = "dyna/merge-trial/1"
	directory := filepath.Join(f.e.s.Store.Root, "trials", "review-1")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "pr-1.patch"), []byte("old input"), 0600); err != nil {
		t.Fatal(err)
	}
	closed := 0
	f.e.s.CloseReviewer = func(_ context.Context, id string) error {
		if id != session {
			t.Fatalf("closed session %q", id)
		}
		closed++
		return nil
	}
	f.e.s.StartAgent = func(subagent.StartOptions) (subagent.Info, error) {
		t.Fatal("resumed batch started another review agent")
		return subagent.Info{}, nil
	}
	if err := f.e.s.Store.update(func(st *State) error {
		b := state.MergeTrial{Branch: branch, Base: f.main, DefaultBranch: "main", Phase: "reviewing", Created: f.e.clock}
		for _, n := range []int{1, 2} {
			b.Changes = append(b.Changes, trialChange(*st.Records[fmt.Sprintf("%s#%d", repo, n)]))
		}
		raw, err := json.Marshal(b)
		if err != nil {
			return err
		}
		var old map[string]json.RawMessage
		if err := json.Unmarshal(raw, &old); err != nil {
			return err
		}
		old["review"] = json.RawMessage(`{"session":"` + session + `","phase":"running","started":"2026-10-17T12:00:00Z"}`)
		raw, err = json.Marshal(old)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &b); err != nil {
			return err
		}
		st.MergeTrials = map[string]*state.MergeTrial{repo: &b}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, err := f.e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	b := st.MergeTrials[repo]
	if b == nil || b.Phase != "running" || f.builds != 1 || closed != 1 {
		t.Fatalf("saved batch did not resume: batch=%+v builds=%d closed=%d", b, f.builds, closed)
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"review"`) {
		t.Fatalf("retained old metadata: %s", raw)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("old input directory remains: %v", err)
	}
}
