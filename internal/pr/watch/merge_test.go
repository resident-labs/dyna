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
	"strconv"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// The regular HTTP fixture builds each response. This runner only adds remote
// merge/edit effects and selects one fixture per PR in the batched observation.
func mergeEnv(t *testing.T, numbers ...int) (*testEnv, map[int]*fixture, *[]int) {
	t.Helper()
	e := newEnv(t)
	rooms := &room.Store{Root: t.TempDir()}
	rm, err := rooms.Create(room.CreateInput{Name: "merge-work"})
	if err != nil {
		t.Fatal(err)
	}
	e.author.RoomID = rm.ID
	if err := rooms.UpdatePreferences(rm.ID, func(r *room.Record) error { r.GitHubAccess = "read-write"; return nil }); err != nil {
		t.Fatal(err)
	}
	e.s.Notify = notify.New(rooms, subagent.NewManager())
	e.s.Deliver = func(_ context.Context, _ Record, n Notice) (string, error) {
		e.sent = append(e.sent, n)
		post, err := rooms.Post(rm.ID, room.PostInput{ID: n.ID, From: "system", To: []string{rm.Name}, Text: n.Text})
		return post.ID, err
	}
	e.s.CleanupMerged = func(context.Context, Record, string) error { return nil }
	fixtures := map[int]*fixture{}
	merged := &[]int{}
	st := State{Version: 1, Records: map[string]*Record{}, Reviewers: map[string]Reviewer{}}
	for i, n := range numbers {
		ref := Ref{"github.com", "owner/repo", n}
		head := fmt.Sprintf("%040x", n)
		f := e.f
		f.Pull = Pull{Number: n, State: "open"}
		f.Pull.Head.SHA, f.Pull.Head.Ref = head, fmt.Sprint("feature-", n)
		f.Pull.Base.Ref, f.Pull.Base.Repo.DefaultBranch = "main", "main"
		f.Checks = []Check{actionsCheck(Check{ID: int64(n), Name: "unit", Status: "completed", Conclusion: "success"})}
		f.Runs = nil
		fixtures[n] = &f
		r := Record{Key: ref.Key(), ChangeID: ref.Key(), Kind: "github", Host: ref.Host, Repo: ref.Repo, Number: n, URL: ref.URL(),
			RoomID: rm.ID, Author: e.author.ID, Dir: e.author.Dir, Head: head, Branch: f.Pull.Head.Ref, BaseRef: "main",
			State: "open", Watching: true, Verdict: "approve", RoundHead: head, CI: "passed", CICleanupDone: true,
			Created: e.clock.Add(time.Duration(i) * time.Second)}
		r.Polling.CIVersion = "SUCCESS"
		r.PolicyOverride.RequiredChecks = []string{}
		r.CINoJUnitWarned = true
		st.Records[r.Key] = &r
	}
	if err := os.MkdirAll(e.s.Store.Root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := state.Write(e.s.Store.Root, st); err != nil {
		t.Fatal(err)
	}
	raw := e.s.GH.run
	var active *fixture
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		if filepath.Base(cmd.Path) == "git" {
			return errors.New("use mergeTrialEnv for automatic trial tests")
		}
		args := cmd.Args[1:]
		joined := strings.Join(args, " ")
		if len(args) > 1 && args[0] == "pr" {
			switch args[1] {
			case "merge":
				n, _ := strconv.Atoi(args[len(args)-1][strings.LastIndex(args[len(args)-1], "/")+1:])
				f := fixtures[n]
				if !strings.Contains(joined, "--merge --match-head-commit "+f.Pull.Head.SHA) {
					return fmt.Errorf("merge lacks head guard: %v", args)
				}
				*merged = append(*merged, n)
				f.Pull.State, f.Pull.Merged = "closed", true
				return nil
			case "edit":
				n, _ := strconv.Atoi(args[2][strings.LastIndex(args[2], "/")+1:])
				fixtures[n].Pull.Base.Ref = args[len(args)-1]
				return nil
			}
		}
		if strings.Contains(joined, " graphql ") || (len(args) > 1 && args[1] == "graphql") {
			nodes := map[string]any{"defaultBranchRef": map[string]any{"name": "main", "target": map[string]any{"oid": strings.Repeat("d", 40), "committedDate": e.clock, "workflows": map[string]any{"object": map[string]any{"entries": []any{map[string]any{"name": "ci.yml", "type": "blob", "object": map[string]any{"text": "on: [push]", "isTruncated": false}}}}}, "statusCheckRollup": map[string]any{"state": "SUCCESS", "contexts": map[string]int{"totalCount": 2}}}}}
			for n, f := range fixtures {
				data := fakeHeadBatch(*f, args).(map[string]any)["data"].(map[string]any)
				if repo, ok := data["r0"].(map[string]any); ok {
					nodes[fmt.Sprint("p", n)] = repo[fmt.Sprint("p", n)]
				}
			}
			fmt.Fprint(cmd.Stdout, "HTTP/2.0 200 OK\r\n\r\n")
			return json.NewEncoder(cmd.Stdout).Encode(map[string]any{"data": map[string]any{"r0": nodes}})
		}
		if strings.Contains(joined, "pulls?state=open") {
			var open []Pull
			for _, f := range fixtures {
				if f.Pull.State == "open" {
					open = append(open, f.Pull)
				}
			}
			fmt.Fprint(cmd.Stdout, "HTTP/2.0 200 OK\r\n\r\n")
			return json.NewEncoder(cmd.Stdout).Encode(open)
		}
		for n, f := range fixtures {
			if strings.Contains(joined, f.Pull.Head.SHA) || strings.Contains(joined, fmt.Sprintf("/pulls/%d ", n)) || strings.HasSuffix(joined, fmt.Sprintf("/pulls/%d", n)) {
				active = f
				break
			}
		}
		if active == nil {
			return fmt.Errorf("unexpected GitHub request: %v", args)
		}
		e.f = *active
		e.write()
		return raw(ctx, cmd)
	}
	return e, fixtures, merged
}

func TestRepairMergeRechecksFreshCIAndPolicy(t *testing.T) {
	for _, which := range []string{"pending", "failed", "policy", "changed-head", "draft", "confirmation-error"} {
		t.Run(which, func(t *testing.T) {
			e, fs, merged := mergeEnv(t, 1)
			if which == "policy" {
				// The current CI policy selects this external check. Before the
				// policy change it is ignored by the shared CI implementation.
				fs[1].Checks = append(fs[1].Checks, Check{Name: "new-required-check", Status: "completed", Conclusion: "failure"})
			}
			raw := e.s.GH.run
			e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
				joined := strings.Join(cmd.Args, " ")
				if which == "confirmation-error" && strings.Contains(joined, "graphql") {
					return errors.New("confirmation failed")
				}
				if strings.Contains(joined, "/pulls/1") && !strings.Contains(joined, "pulls?") {
					switch which {
					case "pending":
						fs[1].Checks[0].Status, fs[1].Checks[0].Conclusion = "in_progress", ""
					case "failed":
						fs[1].Checks[0].Conclusion = "failure"
					case "changed-head":
						fs[1].Pull.Head.SHA = strings.Repeat("f", 40)
					case "policy":
						if err := e.s.Store.update(func(st *State) error {
							st.Records["github.com/owner/repo#1"].PolicyOverride.RequiredChecks = []string{"new-required-check"}
							return nil
						}); err != nil {
							return err
						}
					}
				}
				return raw(ctx, cmd)
			}
			if which == "draft" {
				fs[1].Draft = true
			}
			err := e.s.MergeFix(t.Context(), "github.com/owner/repo#1")
			if which == "confirmation-error" && err == nil {
				t.Fatal("confirmation failure did not stop merge")
			}
			if which != "confirmation-error" && err != nil && !strings.Contains(err.Error(), "not eligible") {
				t.Fatal(err)
			}
			if len(*merged) != 0 {
				t.Fatalf("merged with %s: %v", which, *merged)
			}
		})
	}
}

func TestRepairMergeRetargetsUntrackedChildren(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			e, fs, merged := mergeEnv(t, 1, 2)
			if err := e.s.Store.update(func(st *State) error { delete(st.Records, "github.com/owner/repo#2"); return nil }); err != nil {
				t.Fatal(err)
			}
			fs[2].Pull.Base.Ref = "feature-1"
			raw := e.s.GH.run
			e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
				if len(cmd.Args) > 2 && cmd.Args[1] == "pr" && cmd.Args[2] == "edit" && fail {
					return errors.New("retarget failed")
				}
				if len(cmd.Args) > 2 && cmd.Args[1] == "pr" && cmd.Args[2] == "merge" && fs[2].Pull.Base.Ref != "main" {
					t.Error("merged before retargeting child")
				}
				return raw(ctx, cmd)
			}
			err := e.s.MergeFix(t.Context(), "github.com/owner/repo#1")
			if fail {
				if err == nil || len(*merged) != 0 {
					t.Fatalf("failed retarget: %v %v", *merged, err)
				}
			} else if err != nil || !reflect.DeepEqual(*merged, []int{1}) {
				t.Fatalf("merge: %v %v", *merged, err)
			}
		})
	}
}

func TestAutomaticMergeConflictUsesRoomLogDedupe(t *testing.T) {
	e, fs, merged := mergeEnv(t, 1)
	fs[1].Mergeability = "CONFLICTING"
	for range 2 {
		if _, err := e.s.MergeTick(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if len(e.sent) != 1 || !strings.Contains(e.sent[0].Text, "rebase onto main") {
		t.Fatalf("conflict notices: %+v", e.sent)
	}
	if len(*merged) != 0 {
		t.Fatalf("merged conflict: %v", *merged)
	}
	// Reconstruct the service: the indexed room post, not an in-memory flag,
	// still suppresses the notice.
	s := *e.s
	if _, err := s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(e.sent) != 1 {
		t.Fatalf("repeated conflict notice: %+v", e.sent)
	}
	fs[1].Pull.Head.SHA = strings.Repeat("f", 40)
	if _, err := s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(e.sent) != 2 {
		t.Fatalf("new head did not get a conflict notice: %+v", e.sent)
	}
}

func TestDraftPRDoesNotSendMergeConflict(t *testing.T) {
	e, fs, _ := mergeEnv(t, 1)
	fs[1].Draft = true
	fs[1].Mergeability = "CONFLICTING"
	if _, err := e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(e.sent) != 0 {
		t.Fatalf("draft PR sent a conflict notice: %+v", e.sent)
	}
	fs[1].Draft = false
	if _, err := e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(e.sent) != 1 {
		t.Fatalf("ready PR missed its conflict notice: %+v", e.sent)
	}
}

func TestPlanAndMergeConflictNoticesShareIdentity(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint("legacy=", legacy), func(t *testing.T) {
			e, _, _ := mergeEnv(t, 1)
			const key = "github.com/owner/repo#1"
			if err := e.s.Store.update(func(st *State) error {
				r := st.Records[key]
				token := "merge-conflict"
				if legacy {
					token += ":oldbase:" + r.Head + ":code.txt"
				}
				notice(r, r.Author, changeLabel(*r)+" needs a rebase before merging onto oldbase. Conflicting files: code.txt.", token)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := e.s.flush(t.Context(), key); err != nil {
				t.Fatal(err)
			}
			r, err := e.s.Store.Get(key)
			if err != nil {
				t.Fatal(err)
			}
			if err := e.s.postMergeConflict(t.Context(), r, "main"); err != nil {
				t.Fatal(err)
			}
			if len(e.sent) != 1 {
				t.Fatalf("plan and merge worker repeated the same conflict: %+v", e.sent)
			}
		})
	}
}

func TestRepairMergeHonorsRoomPauseAtActionBoundaries(t *testing.T) {
	for _, during := range []bool{false, true} {
		t.Run(fmt.Sprint(during), func(t *testing.T) {
			e, _, merged := mergeEnv(t, 1)
			pause := func() {
				if _, err := e.s.Notify.Rooms.SetSessionsPaused(t.Context(), e.author.RoomID, true); err != nil {
					t.Fatal(err)
				}
			}
			raw := e.s.GH.run
			e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
				if during && strings.Contains(strings.Join(cmd.Args, " "), "/pulls/1") {
					pause()
				}
				if !during {
					t.Errorf("paused room made a GitHub request: %v", cmd.Args)
				}
				return raw(ctx, cmd)
			}
			if !during {
				pause()
			}
			if err := e.s.MergeFix(t.Context(), "github.com/owner/repo#1"); err != nil && !strings.Contains(err.Error(), "not eligible") {
				t.Fatal(err)
			}
			if len(*merged) != 0 {
				t.Fatalf("paused room merged: %v", *merged)
			}
		})
	}
}

func TestRepairMergeFailedFinalCheckDoesNotRetargetChildren(t *testing.T) {
	e, fs, merged := mergeEnv(t, 1, 2)
	if err := e.s.Store.update(func(st *State) error { delete(st.Records, "github.com/owner/repo#2"); return nil }); err != nil {
		t.Fatal(err)
	}
	fs[2].Pull.Base.Ref = "feature-1"
	raw := e.s.GH.run
	edits := 0
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		if len(cmd.Args) > 2 && cmd.Args[1] == "pr" && cmd.Args[2] == "edit" {
			edits++
		}
		if strings.Contains(strings.Join(cmd.Args, " "), "/pulls/1") {
			fs[1].Checks[0].Conclusion = "failure"
		}
		return raw(ctx, cmd)
	}
	if err := e.s.MergeFix(t.Context(), "github.com/owner/repo#1"); err != nil && !strings.Contains(err.Error(), "not eligible") {
		t.Fatal(err)
	}
	if len(*merged) != 0 || edits != 0 || fs[2].Pull.Base.Ref != "feature-1" {
		t.Fatalf("failed final check changed the stack: merges=%v edits=%d child base=%s", *merged, edits, fs[2].Pull.Base.Ref)
	}
}

func TestRepairMergeRejectedPublicationRestoresChildren(t *testing.T) {
	for _, rollbackFails := range []bool{false, true} {
		t.Run(fmt.Sprint(rollbackFails), func(t *testing.T) {
			e, fs, merged := mergeEnv(t, 1, 2)
			if err := e.s.Store.update(func(st *State) error { delete(st.Records, "github.com/owner/repo#2"); return nil }); err != nil {
				t.Fatal(err)
			}
			raw := e.s.GH.run
			e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
				if len(cmd.Args) > 2 && cmd.Args[1] == "pr" {
					if cmd.Args[2] == "merge" {
						return errors.New("merge rejection")
					}
					if rollbackFails && cmd.Args[2] == "edit" && cmd.Args[len(cmd.Args)-1] == "feature-1" {
						return errors.New("rollback rejection")
					}
				}
				return raw(ctx, cmd)
			}
			for range 2 {
				// A normal watcher pass confirms the same-head CI before a
				// later merge retry. Recreate that observed fixture fact here.
				if err := e.s.Store.update(func(st *State) error {
					r := st.Records["github.com/owner/repo#1"]
					r.CI, r.Polling.CIVersion = "passed", "SUCCESS"
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				fs[2].Pull.Base.Ref = "feature-1"
				err := e.s.MergeFix(t.Context(), "github.com/owner/repo#1")
				if err == nil || !strings.Contains(err.Error(), "merge rejection") {
					t.Fatalf("merge error lost: %v", err)
				}
				if rollbackFails {
					if !strings.Contains(err.Error(), "rollback rejection") {
						t.Fatalf("rollback error lost: %v", err)
					}
				} else if fs[2].Pull.Base.Ref != "feature-1" {
					t.Fatalf("child was not restored: %s", fs[2].Pull.Base.Ref)
				}
			}
			if len(*merged) != 0 {
				t.Fatalf("rejected publication merged: %v", *merged)
			}
			notices := 0
			for _, n := range e.sent {
				if strings.Contains(n.Text, "could not restore child PR bases") {
					notices++
				}
			}
			want := 0
			if rollbackFails {
				want = 1
			}
			if notices != want {
				t.Fatalf("rollback notices=%d, want %d; %+v", notices, want, e.sent)
			}
		})
	}
}

func TestRepairMergeConfirmedPublicationKeepsChildrenRetargeted(t *testing.T) {
	e, fs, merged := mergeEnv(t, 1, 2)
	if err := e.s.Store.update(func(st *State) error { delete(st.Records, "github.com/owner/repo#2"); return nil }); err != nil {
		t.Fatal(err)
	}
	fs[2].Pull.Base.Ref = "feature-1"
	raw := e.s.GH.run
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		err := raw(ctx, cmd)
		if err == nil && len(cmd.Args) > 2 && cmd.Args[1] == "pr" && cmd.Args[2] == "merge" {
			return errors.New("transport failed after merge")
		}
		return err
	}
	if err := e.s.MergeFix(t.Context(), "github.com/owner/repo#1"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*merged, []int{1}) || fs[2].Pull.Base.Ref != "main" {
		t.Fatalf("confirmed merge rolled back children: %v %s", *merged, fs[2].Pull.Base.Ref)
	}
}

func TestRepairMergePartialRetargetFailureRestoresChildren(t *testing.T) {
	e, fs, merged := mergeEnv(t, 1, 2, 3)
	if err := e.s.Store.update(func(st *State) error {
		delete(st.Records, "github.com/owner/repo#2")
		delete(st.Records, "github.com/owner/repo#3")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fs[2].Pull.Base.Ref, fs[3].Pull.Base.Ref = "feature-1", "feature-1"
	raw := e.s.GH.run
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		if len(cmd.Args) > 2 && cmd.Args[1] == "pr" && cmd.Args[2] == "edit" && strings.HasSuffix(cmd.Args[3], "/3") && cmd.Args[len(cmd.Args)-1] == "main" {
			return errors.New("retarget rejection")
		}
		return raw(ctx, cmd)
	}
	if err := e.s.MergeFix(t.Context(), "github.com/owner/repo#1"); err == nil {
		t.Fatal("retarget error lost")
	}
	if len(*merged) != 0 || fs[2].Pull.Base.Ref != "feature-1" || fs[3].Pull.Base.Ref != "feature-1" {
		t.Fatalf("partial edits escaped: %v %s %s", *merged, fs[2].Pull.Base.Ref, fs[3].Pull.Base.Ref)
	}
}

func TestRepairMergeRechecksEligibilityAfterChildEdits(t *testing.T) {
	for _, why := range []string{"ci", "pause", "approval", "draft", "head"} {
		t.Run(why, func(t *testing.T) {
			e, fs, merged := mergeEnv(t, 1, 2)
			if err := e.s.Store.update(func(st *State) error { delete(st.Records, "github.com/owner/repo#2"); return nil }); err != nil {
				t.Fatal(err)
			}
			fs[2].Pull.Base.Ref = "feature-1"
			raw := e.s.GH.run
			e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
				err := raw(ctx, cmd)
				if err == nil && len(cmd.Args) > 2 && cmd.Args[1] == "pr" && cmd.Args[2] == "edit" && cmd.Args[len(cmd.Args)-1] == "main" {
					switch why {
					case "ci":
						fs[1].Checks[0].Conclusion = "failure"
					case "pause":
						_, err = e.s.Notify.Rooms.SetSessionsPaused(ctx, e.author.RoomID, true)
					case "approval":
						err = e.s.Store.update(func(st *State) error {
							r := st.Records["github.com/owner/repo#1"]
							r.Verdict = "changes"
							return nil
						})
					case "draft":
						fs[1].Draft = true
					case "head":
						fs[1].Pull.Head.SHA = strings.Repeat("f", 40)
					}
				}
				return err
			}
			if err := e.s.MergeFix(t.Context(), "github.com/owner/repo#1"); err != nil && !strings.Contains(err.Error(), "not eligible") {
				t.Fatal(err)
			}
			if len(*merged) != 0 || fs[2].Pull.Base.Ref != "feature-1" {
				t.Fatalf("eligibility changed during child edits: why=%s merges=%v child base=%s", why, *merged, fs[2].Pull.Base.Ref)
			}
		})
	}
}

func TestRepairMergeUnknownOutcomeDoesNotRestoreChildren(t *testing.T) {
	e, fs, merged := mergeEnv(t, 1, 2)
	if err := e.s.Store.update(func(st *State) error { delete(st.Records, "github.com/owner/repo#2"); return nil }); err != nil {
		t.Fatal(err)
	}
	fs[2].Pull.Base.Ref = "feature-1"
	raw := e.s.GH.run
	published, confirm := false, false
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		if published && !confirm && strings.Contains(strings.Join(cmd.Args, " "), "/pulls/1") {
			return errors.New("confirmation unavailable")
		}
		err := raw(ctx, cmd)
		if err == nil && len(cmd.Args) > 2 && cmd.Args[1] == "pr" && cmd.Args[2] == "merge" {
			published = true
		}
		return err
	}
	if err := e.s.MergeFix(t.Context(), "github.com/owner/repo#1"); err == nil {
		t.Fatal("unknown merge outcome was not reported")
	}
	if !reflect.DeepEqual(*merged, []int{1}) || fs[2].Pull.Base.Ref != "main" {
		t.Fatalf("unknown outcome restored child: %v %s", *merged, fs[2].Pull.Base.Ref)
	}
	unknownNotices := 0
	for _, n := range e.sent {
		if strings.Contains(n.Text, "merge outcome") {
			unknownNotices++
			if n.Target != "" || !strings.Contains(n.Text, fs[2].Pull.Head.Ref) && !strings.Contains(n.Text, "/pull/2") {
				t.Fatalf("owner recovery notice: %+v", n)
			}
		}
	}
	if unknownNotices != 1 {
		t.Fatalf("unknown outcome notices: %+v", e.sent)
	}

	confirm = true
	if _, err := e.s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	r, err := e.s.Store.Get("github.com/owner/repo#1")
	if err != nil || r.State != "merged" || r.Watching || fs[2].Pull.Base.Ref != "main" {
		t.Fatalf("later observation: %+v %v child=%s", r, err, fs[2].Pull.Base.Ref)
	}
}

func TestRepairMergeUnknownOpenParentKeepsOneOwnerRecoveryNotice(t *testing.T) {
	e, fs, merged := mergeEnv(t, 1, 2)
	if err := e.s.Store.update(func(st *State) error { delete(st.Records, "github.com/owner/repo#2"); return nil }); err != nil {
		t.Fatal(err)
	}
	fs[2].Pull.Base.Ref = "feature-1"
	raw := e.s.GH.run
	published, confirm := false, false
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		if published && !confirm && strings.Contains(strings.Join(cmd.Args, " "), "/pulls/1") {
			return errors.New("confirmation unavailable")
		}
		if len(cmd.Args) > 2 && cmd.Args[1] == "pr" && cmd.Args[2] == "merge" {
			published = true
			return errors.New("merge transport unavailable")
		}
		return raw(ctx, cmd)
	}
	for range 2 {
		if err := e.s.Store.update(func(st *State) error {
			r := st.Records["github.com/owner/repo#1"]
			r.CI, r.Polling.CIVersion = "passed", "SUCCESS"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := e.s.MergeFix(t.Context(), "github.com/owner/repo#1"); err == nil {
			t.Fatal("unknown outcome error lost")
		}
	}
	confirm = true
	if err := e.s.Store.update(func(st *State) error { st.Records["github.com/owner/repo#1"].Verdict = "changes"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := e.s.MergeFix(t.Context(), "github.com/owner/repo#1"); err != nil && !strings.Contains(err.Error(), "not eligible") {
		t.Fatal(err)
	}
	if len(*merged) != 0 || fs[2].Pull.Base.Ref != "main" {
		t.Fatalf("unknown outcome changed child: %v %s", *merged, fs[2].Pull.Base.Ref)
	}
	notices := 0
	for _, n := range e.sent {
		if strings.Contains(n.Text, "merge outcome") {
			notices++
			if n.Target != "" || !strings.Contains(n.Text, "/pull/1") || !strings.Contains(n.Text, "/pull/2") {
				t.Fatalf("owner notice lost recovery facts: %+v", n)
			}
		}
	}
	if notices != 1 {
		t.Fatalf("unknown outcome posted %d notices", notices)
	}
}

func TestRepairMergeSuccessfulPublicationWithOpenReadIsUnknown(t *testing.T) {
	e, fs, merged := mergeEnv(t, 1, 2)
	if err := e.s.Store.update(func(st *State) error { delete(st.Records, "github.com/owner/repo#2"); return nil }); err != nil {
		t.Fatal(err)
	}
	fs[2].Pull.Base.Ref = "feature-1"
	raw := e.s.GH.run
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		// GitHub accepted the request, but the confirmation read still reports open.
		if len(cmd.Args) > 2 && cmd.Args[1] == "pr" && cmd.Args[2] == "merge" {
			return nil
		}
		return raw(ctx, cmd)
	}
	if err := e.s.MergeFix(t.Context(), "github.com/owner/repo#1"); err == nil {
		t.Fatal("lagging confirmation was not reported")
	}
	if len(*merged) != 0 || fs[2].Pull.Base.Ref != "main" {
		t.Fatalf("lagging confirmation restored child: %v %s", *merged, fs[2].Pull.Base.Ref)
	}
	notices := 0
	for _, n := range e.sent {
		if strings.Contains(n.Text, "merge outcome") {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("unknown outcome notices: %+v", e.sent)
	}
}

func rewriteMergeDefault(e *testEnv, update func(map[string]any)) {
	raw := e.s.GH.run
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		if !strings.Contains(strings.Join(cmd.Args, " "), "graphql") {
			return raw(ctx, cmd)
		}
		var body bytes.Buffer
		output := cmd.Stdout
		cmd.Stdout = &body
		if err := raw(ctx, cmd); err != nil {
			return err
		}
		payload := strings.SplitN(body.String(), "\r\n\r\n", 2)
		var data map[string]any
		if err := json.Unmarshal([]byte(payload[1]), &data); err != nil {
			return err
		}
		target := data["data"].(map[string]any)["r0"].(map[string]any)["defaultBranchRef"].(map[string]any)["target"].(map[string]any)
		update(target)
		fmt.Fprint(output, "HTTP/2.0 200 OK\r\n\r\n")
		return json.NewEncoder(output).Encode(data)
	}
}

func TestDefaultBranchPauseSkipsRemoteWorkAndNotices(t *testing.T) {
	e, _, _ := mergeEnv(t, 1)
	if _, err := e.s.Notify.Rooms.SetSessionsPaused(t.Context(), e.author.RoomID, true); err != nil {
		t.Fatal(err)
	}
	e.s.GH.run = func(context.Context, *exec.Cmd) error { t.Fatal("paused room started GitHub work"); return nil }
	if _, err := e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, err := e.s.MergeStatus(t.Context())
	if err != nil || len(rows) != 1 || rows[0].State != "paused" || rows[0].RoomID != e.author.RoomID {
		t.Fatalf("paused status=%+v error=%v", rows, err)
	}
	if len(e.sent) != 0 {
		t.Fatal("paused room received a default-branch notice")
	}
}

func TestMergeQueueNamesWhatEachPRWaitsFor(t *testing.T) {
	yes := true
	record := func(n int, edit func(*Record)) *Record {
		head := fmt.Sprintf("%040x", n)
		r := &Record{Key: fmt.Sprint("github.com/owner/repo#", n), Kind: "github", Host: "github.com", Repo: "owner/repo", Number: n,
			RoomID: "room_a", Head: head, BaseRef: "main", DefaultBranch: "main", Mergeable: &yes, State: "open", Watching: true,
			Verdict: "approve", RoundHead: head, CI: "passed"}
		r.ChangeID = r.Key
		edit(r)
		return r
	}
	st := State{Records: map[string]*Record{}, RepositoryFacts: map[string]state.RepositoryFacts{
		"github.com/owner/repo": {DefaultBranch: "main", Head: "main-head", Verdict: "passed", CheckedAt: time.Now(), MergeTrialCI: true},
	}}
	for n, edit := range map[int]func(*Record){
		1: func(*Record) {},
		2: func(r *Record) {
			r.CI = "failed"
			r.Checks = []CheckSummary{{Name: "lint", State: "success"}, {Name: "test", State: "failure"}, {Name: "build", State: "failure"}}
		},
		3: func(r *Record) { r.Verdict = "changes" },
		4: func(r *Record) { r.CI = "pending" },
		5: func(r *Record) { r.Verdict = "" },
		6: func(r *Record) { r.RoundHead = "old" },
		7: func(r *Record) { r.HeadStale = true },
		8: func(r *Record) { r.CI = "failed" },
	} {
		r := record(n, edit)
		st.Records[r.Key] = r
	}
	got := map[int][2]string{}
	for _, item := range mergeQueue(st) {
		n, _ := strconv.Atoi(item.ChangeID[strings.LastIndex(item.ChangeID, "#")+1:])
		got[n] = [2]string{item.State, item.Reason}
	}
	want := map[int][2]string{
		1: {"ready", ""},
		2: {"ci-failed", "CI failed: build"},
		3: {"changes", "changes requested"},
		4: {"ci", "waiting for CI"},
		5: {"review", "needs review"},
		6: {"review", "needs review"},
		7: {"waiting", "waiting for a verified head"},
		8: {"ci-failed", "CI failed"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("states:\n got %v\nwant %v", got, want)
	}
}

func TestRoomMergeQueueReadsOnlyStoredFactsOfOneRoom(t *testing.T) {
	e, _, _ := mergeEnv(t, 1, 2)
	other, err := e.s.Notify.Rooms.Create(room.CreateInput{Name: "other-work"})
	if err != nil {
		t.Fatal(err)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	st.Records["github.com/owner/repo#2"].RoomID = other.ID
	if err := state.Write(e.s.Store.Root, st); err != nil {
		t.Fatal(err)
	}
	e.s.GH.run = func(_ context.Context, cmd *exec.Cmd) error {
		t.Errorf("room status made a GitHub request: %v", cmd.Args)
		return errors.New("no GitHub")
	}
	rows, err := e.s.RoomMergeQueue(e.author.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ChangeID != "github.com/owner/repo#1" || rows[0].RoomID != e.author.RoomID {
		t.Fatalf("rows=%+v", rows)
	}
	if _, err := e.s.Notify.Rooms.SetSessionsPaused(t.Context(), e.author.RoomID, true); err != nil {
		t.Fatal(err)
	}
	rows, err = e.s.RoomMergeQueue(e.author.RoomID)
	if err != nil || len(rows) != 1 || rows[0].State != "paused" {
		t.Fatalf("paused rows=%+v err=%v", rows, err)
	}
}

func TestMergeQueueItemsCarryTheMainCIResult(t *testing.T) {
	yes := true
	r := &Record{Key: "github.com/owner/repo#1", ChangeID: "github.com/owner/repo#1", Kind: "github", Host: "github.com", Repo: "owner/repo", Number: 1,
		RoomID: "room_a", Head: "h1", BaseRef: "main", DefaultBranch: "main", Mergeable: &yes, State: "open", Watching: true, CI: "pending"}
	for verdict, want := range map[string]MainCI{
		"passed": {State: "passed", Head: "main-head", Reason: "GitHub CI passed on main at main-head"},
		"failed": {State: "failed", Head: "main-head", Reason: "GitHub CI failed on main at main-head"},
		"":       {State: "pending", Head: "main-head", Reason: "Waiting for completed GitHub CI on main at main-head"},
	} {
		st := State{Records: map[string]*Record{r.Key: r}, RepositoryFacts: map[string]state.RepositoryFacts{
			"github.com/owner/repo": {DefaultBranch: "main", Head: "main-head", Verdict: verdict, CheckedAt: time.Now()},
		}}
		items := mergeQueue(st)
		// A PR that is not ready still carries the result: the Checks tab shows it for every PR.
		if len(items) != 1 || items[0].State != "review" || items[0].Main == nil || *items[0].Main != want {
			t.Fatalf("verdict %q: items=%+v want main %+v", verdict, items, want)
		}
	}
}

func TestPausedMergeStatusExplainsMainWithoutCI(t *testing.T) {
	e, _, _ := mergeEnv(t, 1)
	if _, err := e.s.Notify.Rooms.SetSessionsPaused(t.Context(), e.author.RoomID, true); err != nil {
		t.Fatal(err)
	}
	e.s.GH.run = func(context.Context, *exec.Cmd) error { t.Fatal("paused status contacted GitHub"); return nil }
	for _, view := range []string{"all", "room"} {
		t.Run(view, func(t *testing.T) {
			var rows []MergeQueueItem
			var err error
			if view == "all" {
				rows, err = e.s.MergeStatus(t.Context())
			} else {
				rows, err = e.s.RoomMergeQueue(e.author.RoomID)
			}
			if err != nil || len(rows) != 1 {
				t.Fatalf("rows=%+v err=%v", rows, err)
			}
			if rows[0].Main == nil || rows[0].Main.State != "paused" || rows[0].Main.Reason != "merging paused in this room" {
				t.Fatalf("paused main field=%+v", rows[0].Main)
			}
		})
	}
}

func TestObservedMergeRetargetsChildrenBeforeEndingWatch(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			e, fs, _ := mergeEnv(t, 1, 2)
			key := "github.com/owner/repo#1"
			if err := e.s.Store.update(func(st *State) error { delete(st.Records, "github.com/owner/repo#2"); return nil }); err != nil {
				t.Fatal(err)
			}
			fs[2].Pull.Base.Ref = "feature-1"
			fs[1].Pull.State, fs[1].Pull.Merged = "closed", true
			run := e.s.GH.run
			e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
				if len(cmd.Args) > 2 && cmd.Args[1] == "pr" && cmd.Args[2] == "edit" && fail {
					return errors.New("retarget unavailable")
				}
				return run(ctx, cmd)
			}
			observe := func() error {
				st, err := e.s.Store.Read()
				if err != nil {
					return err
				}
				if _, err := e.s.refreshHeads(t.Context(), st, e.clock, nil); err != nil {
					return err
				}
				observed, err := e.s.Store.Get(key)
				if err != nil {
					return err
				}
				if !observed.Watching || observed.State != "merged" {
					t.Fatalf("head refresh did not retain merged watch: %+v", observed)
				}
				return e.s.process(t.Context(), key, Snapshot{Pull: fs[1].Pull, headConfirmed: true}, "", "", nil, nil, false)
			}
			err := observe()
			r, _ := e.s.Store.Get(key)
			if fail {
				if err == nil || !r.Watching || r.State != "merged" || fs[2].Pull.Base.Ref != "feature-1" {
					t.Fatalf("lost retry after failed retarget: record=%+v child=%s error=%v", r, fs[2].Pull.Base.Ref, err)
				}
				fail = false
				if err = observe(); err != nil {
					t.Fatal(err)
				}
				r, _ = e.s.Store.Get(key)
			}
			if err != nil || r.Watching || r.State != "merged" || fs[2].Pull.Base.Ref != "main" {
				t.Fatalf("merged observation did not retarget child: record=%+v child=%s error=%v", r, fs[2].Pull.Base.Ref, err)
			}
		})
	}
}

func TestObservedMergeRetargetRespectsRoomAndHead(t *testing.T) {
	for _, guard := range []string{"read-only", "paused", "stale-head"} {
		t.Run(guard, func(t *testing.T) {
			e, fs, _ := mergeEnv(t, 1, 2)
			fs[2].Pull.Base.Ref = "feature-1"
			fs[1].Pull.State, fs[1].Pull.Merged = "closed", true
			switch guard {
			case "read-only":
				if err := ghclient.SetAccess(e.s.Notify.Rooms, e.author.RoomID, ghclient.ReadOnly); err != nil {
					t.Fatal(err)
				}
			case "paused":
				if _, err := e.s.Notify.Rooms.SetSessionsPaused(t.Context(), e.author.RoomID, true); err != nil {
					t.Fatal(err)
				}
			case "stale-head":
				fs[1].Pull.Head.SHA = strings.Repeat("f", 40)
			}
			err := e.s.process(t.Context(), "github.com/owner/repo#1", Snapshot{Pull: fs[1].Pull, headConfirmed: true}, "", "", nil, nil, false)
			if (err != nil) != (guard == "paused") {
				t.Fatalf("unexpected error: %v", err)
			}
			if fs[2].Pull.Base.Ref != "feature-1" {
				t.Fatal("retarget bypassed room or head guard")
			}
		})
	}
}
