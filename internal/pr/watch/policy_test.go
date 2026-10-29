package watch

import (
	"context"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/changepolicy"
	"dyna-agent/internal/pr/state"
)

func TestRoomPolicyPreservesLegacySwitchAndEnforcesOwner(t *testing.T) {
	e := newEnv(t)
	if err := e.s.SetReviewEnabled(e.author.RoomID, false); err != nil {
		t.Fatal(err)
	}
	p, err := e.s.RoomChangePolicy(e.author.RoomID)
	if err != nil || p.Review != "off" {
		t.Fatalf("legacy switch: %+v %v", p, err)
	}
	p = changepolicy.Default()
	p.RequiredChecks = []string{"unit"}
	if err = e.s.SetRoomChangePolicy(e.author.RoomID, authority.Actor{Role: authority.Session, Session: e.author.ID}, p); err == nil {
		t.Fatal("agent changed room policy")
	}
	if err = e.s.SetRoomChangePolicy(e.author.RoomID, authority.Actor{Role: authority.Owner}, p); err != nil {
		t.Fatal(err)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if st.Reviewers[e.author.RoomID].Disabled || len(RoomPolicy(st, e.author.RoomID).RequiredChecks) != 1 {
		t.Fatalf("policy not applied: %+v", st)
	}
}
func TestChangeOverrideReasonOwnershipAndReviewGate(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	if err := e.s.SetChangePolicyOverride(r.Key, r.Author, PolicyOverride{RequiredChecks: []string{}}); err == nil {
		t.Fatal("accepted missing reason")
	}
	o := PolicyOverride{RequiredChecks: []string{}, Reason: "Documentation only"}
	if err := e.s.SetChangePolicyOverride(r.Key, r.Author, o); err != nil {
		t.Fatal(err)
	}
	st, _ := e.s.Store.Read()
	r = e.record()
	if !policyReviewReady(st, r) || EffectivePolicy(st, r).RequiredChecks == nil {
		t.Fatal("skip checks still gates on CI")
	}
	o.Review = "off"
	if err := e.s.SetChangePolicyOverride(r.Key, r.Author, o); err != nil {
		t.Fatal(err)
	}
	st, _ = e.s.Store.Read()
	r = e.record()
	if policyReviewReady(st, r) {
		t.Fatal("review off remained eligible")
	}
	if err := e.s.SetChangePolicyOverride(r.Key, r.Author, PolicyOverride{}); err != nil {
		t.Fatal(err)
	}
	if !e.record().PolicyOverride.Empty() {
		t.Fatal("override not cleared")
	}
}
func TestSourceReviewDoesNotRequireCI(t *testing.T) {
	for _, kind := range []string{"local", "github"} {
		for _, ci := range []string{"passed", "pending", "absent", "failed"} {
			st := State{Policies: map[string]ChangePolicy{"room": changepolicy.Default()}}
			if !policyReviewReady(st, Record{Head: "head", RoomID: "room", Kind: kind, CI: state.CIState(ci)}) {
				t.Errorf("%s %s blocked source review", kind, ci)
			}
		}
	}
}
func TestSourceVerdictDoesNotDependOnGitHubCI(t *testing.T) {
	e := newEnv(t)
	e.setCI("success", 1)
	e.write()
	e.track()
	if err := e.s.Configure(e.author.RoomID, Reviewer{Profile: "judge"}); err != nil {
		t.Fatal(err)
	}
	e.tick()
	r := e.record()
	p := changepolicy.Default()
	p.RequiredChecks = []string{"unit"}
	if err := e.s.SetRoomChangePolicy(r.RoomID, authority.Actor{Role: authority.Owner}, p); err != nil {
		t.Fatal(err)
	}
	e.setCI("failure", 2)
	e.write()
	if err := e.s.Verdict(context.Background(), r.Key, r.Reviewer, r.Head, "approve", "Reviewed source", 0); err != nil {
		t.Fatal(err)
	}
}
func TestReviewAssignmentHasNoLocalCheckEvidence(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	if err := e.s.SetChangePolicyOverride(r.Key, r.Author, PolicyOverride{RequiredChecks: []string{}, Reason: "Docs only"}); err != nil {
		t.Fatal(err)
	}
	task, err := e.s.prepareReviewTask(context.Background(), e.record(), "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(task, "no checks were required") || strings.Contains(task, "dyna check") {
		t.Fatalf("skip policy task: %s", task)
	}
}

func TestPolicyOverridesDriveQueueAndLegacySwitch(t *testing.T) {
	e := newEnv(t)
	e.setCI("success", 1)
	r := e.track()
	p := changepolicy.Default()
	p.RequiredChecks = []string{"unit"}
	p.Review = "off"
	if err := e.s.SetRoomChangePolicy(r.RoomID, authority.Actor{Role: authority.Owner}, p); err != nil {
		t.Fatal(err)
	}
	if err := e.s.SetChangePolicyOverride(r.Key, r.Author, PolicyOverride{Review: "on", Reason: "Review this change"}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Configure(r.RoomID, Reviewer{Profile: "judge"}); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if len(e.launches) != 1 || e.record().Reviewer == "" {
		t.Fatalf("override did not enable room queue: %+v", e.record())
	}
	if err := e.s.SetChangePolicyOverride(r.Key, r.Author, PolicyOverride{Review: "off", Reason: "Owner reviewed"}); err != nil {
		t.Fatal(err)
	}
	st, _ := e.s.Store.Read()
	if st.Records[r.Key].Reviewer != "" {
		t.Fatal("disabled review retained current assignment")
	}
	state, _, _ := reviewState(t, e, st, e.record())
	if state != "off" {
		t.Fatalf("state %s", state)
	}
	if err := e.s.SetReviewEnabled(r.RoomID, true); err != nil {
		t.Fatal(err)
	}
	policy, err := e.s.RoomChangePolicy(r.RoomID)
	if err != nil || policy.Review != "on" || len(policy.RequiredChecks) != 1 {
		t.Fatalf("legacy switch changed policy %+v %v", policy, err)
	}
}
func TestSourceReviewStaysQueuedWithoutGitHubCI(t *testing.T) {
	e := newEnv(t)
	st := State{Policies: map[string]ChangePolicy{"room": changepolicy.Default()}, Reviewers: map[string]Reviewer{}}
	for _, kind := range []string{"local", "github"} {
		state, _, reason := reviewState(t, e, st, Record{Head: "head", Kind: kind, CI: "absent", RoomID: "room", Watching: true, State: "open"})
		if state != "queued" || reason != "" {
			t.Fatalf("source review: %s %s", state, reason)
		}
	}
}
func TestRetargetedPullRequestRefreshesBaseAndRequiresNewReview(t *testing.T) {
	e := newEnv(t)
	e.f.Pull.Base.Ref = "main"
	e.setCI("success", 1)
	e.write()
	r := e.track()
	if err := e.s.Configure(r.RoomID, Reviewer{Profile: "judge"}); err != nil {
		t.Fatal(err)
	}
	e.tick()
	r = e.record()
	if err := e.s.Verdict(context.Background(), r.Key, r.Reviewer, r.Head, "approve", "Reviewed current target", 0); err != nil {
		t.Fatal(err)
	}
	e.f.Pull.Base.Ref = "release"
	e.tick()
	after := e.record()
	if after.BaseRef != "release" || after.Verdict != "" || after.Round != r.Round || after.RoundHead != "" {
		t.Fatalf("retarget kept old approval: %+v", after)
	}
}

// Stacked PRs #371-#376 lost their approvals when they moved to main after
// their parent merged, though their own commits did not change.
func TestStackedPullRequestRetargetedToDefaultBranchKeepsApproval(t *testing.T) {
	parentTip, oldTip := strings.Repeat("c", 40), strings.Repeat("d", 40)
	type step struct {
		state, head string
		fresh       bool // polled after the retarget was first seen
	}
	for _, tc := range []struct {
		name   string
		parent []step // parent PR state before each tick; nil means not tracked
		keep   bool
	}{
		{"merged parent", []step{{"merged", parentTip, false}}, true},
		{"merge recorded one poll later", []step{{"open", parentTip, false}, {"merged", parentTip, true}}, true},
		{"reused branch with an older merged PR", []step{{"merged", oldTip, false}}, false},
		{"fresh poll shows the parent still open", []step{{"open", parentTip, false}, {"open", parentTip, true}}, false},
		{"parent never polled again", []step{{"open", parentTip, false}, {"open", parentTip, false}, {"open", parentTip, false}}, false},
		{"untracked parent", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.f.Pull.Base.Ref, e.f.Pull.Base.SHA = "feat/parent", parentTip
			e.f.Pull.Base.Repo.DefaultBranch = "main"
			e.setCI("success", 1)
			e.write()
			r := e.track()
			if err := e.s.Configure(r.RoomID, Reviewer{Profile: "judge"}); err != nil {
				t.Fatal(err)
			}
			e.tick()
			r = e.record()
			if err := e.s.Verdict(context.Background(), r.Key, r.Reviewer, r.Head, "approve", "Reviewed the stacked change", 0); err != nil {
				t.Fatal(err)
			}
			retargeted := e.clock
			setParent := func(p step) {
				checked := retargeted.Add(-time.Hour)
				if p.fresh {
					checked = e.clock
				}
				if err := e.s.Store.update(func(st *State) error {
					parent := *st.Records[r.Key]
					parent.Number, parent.Key = 16, "github.com/owner/repo#16"
					parent.ChangeID, parent.Branch, parent.BaseRef = parent.Key, "feat/parent", "main"
					parent.State, parent.Head, parent.Checked, parent.Watching = state.PRState(p.state), p.head, checked, false
					st.Records[parent.Key] = &parent
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			e.f.Pull.Base.Ref, e.f.Pull.Base.SHA = "main", strings.Repeat("e", 40)
			ticks := max(len(tc.parent), 1)
			for n := 0; n < ticks; n++ {
				if n < len(tc.parent) {
					setParent(tc.parent[n])
				}
				if n == ticks-1 && tc.name == "parent never polled again" {
					e.clock = retargeted.Add(retargetWait)
				}
				// Exercise retarget processing without unrelated watcher phases.
				e.write()
				e.clock = e.clock.Add(time.Minute)
				snap := Snapshot{Pull: e.f.Pull, Checks: e.f.Checks, Runs: e.f.Runs, Statuses: e.f.Statuses}
				ci, key, jobs, _ := snap.CIStatus()
				if err := e.s.process(context.Background(), r.Key, snap, ci, key, jobs, nil, false, e.clock); err != nil {
					t.Fatal(err)
				}
				if n < ticks-1 {
					if waiting := e.record(); waiting.BaseRef != "feat/parent" || waiting.Verdict != "approve" {
						t.Fatalf("tick %d did not wait for the parent: %+v", n+1, waiting)
					}
				}
			}
			after := e.record()
			if after.BaseRef != "main" || !after.BaseRetargetSeen.IsZero() {
				t.Fatalf("base not refreshed: %q seen=%v", after.BaseRef, after.BaseRetargetSeen)
			}
			if !tc.keep {
				if after.Verdict == "approve" && after.Round == r.Round {
					t.Fatalf("retarget kept an approval that does not cover the parent's commits: %+v", after)
				}
				return
			}
			if after.Verdict != "approve" || after.RoundHead != r.Head || after.Round != r.Round {
				t.Fatalf("retarget to the default branch dropped the approval: %+v", after)
			}

		})
	}
}

func TestReopeningLocalChangeWithNewBaseRequiresReview(t *testing.T) {
	e := newEnv(t)
	r, dir := localChange(t, e)
	changeGit(t, dir, "branch", "release", "main")
	if err := e.s.Store.update(func(st *State) error {
		live := st.Records[r.Key]
		live.Verdict = "approve"
		live.RoundHead = live.Head
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	updated, _, err := e.s.OpenChange(context.Background(), authority.Actor{Role: authority.Owner}, e.author, OpenChangeOptions{Dir: dir, Base: "release"})
	if err != nil || updated.BaseRef != "release" || updated.Verdict != "" || updated.RoundHead != "" {
		t.Fatalf("base change %+v %v", updated, err)
	}
}

func TestDisabledAssignmentCleanupVisitsRecords(t *testing.T) {
	for _, verdict := range []state.ReviewVerdict{"", state.VerdictApprove} {
		e := newEnv(t)
		r := e.track()
		if err := e.s.Store.update(func(st *State) error {
			live := st.Records[r.Key]
			live.Reviewer = "reviewer"
			live.RoundHead = live.Head
			live.Verdict = verdict
			live.Notices = []Notice{{ID: "assignment", Target: live.Reviewer, Text: "Assignment input: review this change"}}
			st.Records["other"] = &Record{Key: "other", RoomID: r.RoomID, State: "open", Reviewer: "other-reviewer"}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := e.s.SetChangePolicyOverride(r.Key, r.Author, PolicyOverride{Review: "off", Reason: "Owner reviewed"}); err != nil {
			t.Fatal(err)
		}
		st, _ := e.s.Store.Read()
		live := st.Records[r.Key]
		if live.Notices[0].SupersededBy != "review-disabled-by-policy" {
			t.Fatal("assignment notice remains")
		}
		if verdict == "" && (live.Reviewer != "" || live.RoundHead != "") {
			t.Fatal("disabled assignment remains")
		}
		if verdict != "" && live.Verdict != verdict {
			t.Fatal("cleanup removed verdict")
		}
		if st.Records["other"].Reviewer != "other-reviewer" {
			t.Fatal("cleanup changed another assignment")
		}
	}
}

// The service must use the supplied actor even in an owner or Root process.
func TestSetRoomChangePolicyRefusesActorBeforeStore(t *testing.T) {
	for _, processMode := range []string{"", "root", "subagent", "workflow"} {
		t.Run(processMode, func(t *testing.T) {
			t.Setenv("DYNA_MODE", processMode)
			s := &Service{}
			if err := s.SetRoomChangePolicy("", authority.Actor{Role: authority.Owner}, ChangePolicy{}); err == nil || err.Error() != "room is required" {
				t.Fatalf("empty actor must reach room validation, got %v", err)
			}
			err := s.SetRoomChangePolicy("room", authority.Actor{Role: authority.Session, Session: "session-actor"}, ChangePolicy{})
			if err == nil || !strings.HasPrefix(err.Error(), "SetRoomChangePolicy is owner or root only; ") {
				t.Fatalf("want formatted authority refusal, got %v", err)
			}
		})
	}
}
