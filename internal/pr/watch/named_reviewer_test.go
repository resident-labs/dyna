package watch

import (
	"context"
	"dyna-agent/internal/sessionlife/recordtest"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

const namedReviewerID = "ses_20261017-000005-eeeeeeee"

func namedReviewEnv(t *testing.T) (*testEnv, Record) {
	t.Helper()
	e := newEnv(t)
	e.setCI("success", 1)
	e.write()
	r := e.track()
	recordtest.Write(t, subagent.DefaultStore().Root, namedReviewerID, subagent.Meta{ID: namedReviewerID, Name: "arch-review", RoomID: r.RoomID, Room: e.author.Room})
	// Named review remains available while the automatic pool is off.
	if err := e.s.SetReviewEnabled(r.RoomID, false); err != nil {
		t.Fatal(err)
	}
	policy, err := e.s.RoomChangePolicy(r.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	other := "ses_20261017-000006-ffffffff"
	recordtest.Write(t, subagent.DefaultStore().Root, other, subagent.Meta{ID: other, Name: "thoroughly-idiomatic", RoomID: r.RoomID, Room: e.author.Room})
	policy.Reviewers = []string{namedReviewerID, other}
	if err = e.s.SetRoomChangePolicy(r.RoomID, authority.Actor{Role: authority.Owner}, policy); err != nil {
		t.Fatal(err)
	}
	e.tick()
	return e, e.record()
}

func TestNamedReviewerRecordsExactHeadWithoutPoolAssignment(t *testing.T) {
	e, r := namedReviewEnv(t)
	if r.Reviewer != "" || r.RoundHead != "" || len(e.launches) != 0 {
		t.Fatal("setup acquired a pool assignment")
	}
	if err := e.s.Verdict(context.Background(), r.Key, namedReviewerID, r.Head, "approve", "Reviewed exact head", 0); err != nil {
		t.Fatal(err)
	}
	got := e.record()
	if got.Verdict != "approve" || got.RoundHead != r.Head || got.Reviewer != "" || len(e.launches) != 0 {
		t.Fatalf("named verdict changed pool assignment or lost the head: %+v", got)
	}
	if len(got.ReviewRounds) != 1 || got.ReviewRounds[0].Reviewer != namedReviewerID {
		t.Fatal("verdict lost the authenticated reviewer")
	}
	// Repository capability is a separate confirmed observation.
	if err := e.s.Store.update(func(st *State) error {
		st.RepositoryFacts = map[string]state.RepositoryFacts{defaultRepo(r): {DefaultBranch: "main", MergeTrialCI: true, CheckedAt: e.clock}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.s.Notify = notify.New(room.DefaultStore(), subagent.NewManager())
	items, err := e.s.MergeStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ChangeID == r.Key {
			if item.State != "ready" || item.Head != r.Head {
				t.Fatalf("merge status did not count named approval: %+v", item)
			}
			return
		}
	}
	t.Fatalf("merge status omitted the PR: %+v", items)
}

func TestNamedReviewerRosterChecksCanonicalRoomAndRoot(t *testing.T) {
	e := newEnv(t)
	policy, err := e.s.RoomChangePolicy(e.author.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	policy.Reviewers = []string{namedReviewerID}
	for _, tc := range []struct {
		name string
		info subagent.Info
	}{
		{"wrong room", subagent.Info{Meta: subagent.Meta{ID: namedReviewerID, RoomID: "other", Room: "other"}, Status: subagent.StatusIdle}},
		{"closed", subagent.Info{Meta: subagent.Meta{ID: namedReviewerID, RoomID: e.author.RoomID, Room: e.author.Room, Closed: time.Now()}, Status: subagent.StatusClosed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recordtest.Write(t, subagent.DefaultStore().Root, namedReviewerID, tc.info.Meta)
			if err := e.s.SetRoomChangePolicy(e.author.RoomID, authority.Actor{Role: authority.Owner}, policy); err == nil {
				t.Fatal("accepted invalid named reviewer")
			}
		})
	}
	recordtest.Write(t, subagent.DefaultStore().Root, namedReviewerID, subagent.Meta{ID: namedReviewerID, RoomID: e.author.RoomID, Room: e.author.Room})
	if err := e.s.SetRoomChangePolicy(e.author.RoomID, authority.Actor{Role: authority.Session, Session: e.author.ID}, policy); err == nil {
		t.Fatal("session granted review authority")
	}
	policy.Reviewers = []string{"arch-review"}
	if err := e.s.SetRoomChangePolicy(e.author.RoomID, authority.Actor{Role: authority.Owner}, policy); err == nil {
		t.Fatal("a label granted review authority")
	}
	policy.Reviewers = []string{namedReviewerID, namedReviewerID}
	if err := e.s.SetRoomChangePolicy(e.author.RoomID, authority.Actor{Role: authority.Owner}, policy); err == nil {
		t.Fatal("accepted duplicate reviewer IDs")
	}
}

func TestNamedReviewerRejectsUnlistedSelfAndStaleHead(t *testing.T) {
	e, r := namedReviewEnv(t)
	recordtest.Write(t, subagent.DefaultStore().Root, "ses_20261017-000007-11111111", subagent.Meta{ID: "ses_20261017-000007-11111111", Name: "arch-review", RoomID: r.RoomID, Room: e.author.Room})
	author := e.author
	author.Status = subagent.StatusIdle
	recordtest.Write(t, subagent.DefaultStore().Root, r.Author, author.Meta)
	policy, _ := e.s.RoomChangePolicy(r.RoomID)
	policy.Reviewers = append(policy.Reviewers, r.Author)
	if err := e.s.SetRoomChangePolicy(r.RoomID, authority.Actor{Role: authority.Owner}, policy); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, actor, head string }{
		{"unlisted", "ses_20261017-000007-11111111", r.Head},
		{"author", r.Author, r.Head},
		{"missing head", namedReviewerID, ""},
		{"stale head", namedReviewerID, strings.Repeat("b", 40)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_ = e.s.Verdict(context.Background(), r.Key, tc.actor, tc.head, "approve", "Cannot authorize this head", 0)
			if e.record().Verdict != "" {
				t.Fatal("refused caller or head recorded an approval")
			}
		})
	}
}

func TestNamedReviewerRechecksRosterBeforeCommit(t *testing.T) {
	e, r := namedReviewEnv(t)
	run := e.s.GH.run
	revoked := false
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		if !revoked {
			revoked = true
			policy, err := e.s.RoomChangePolicy(r.RoomID)
			if err != nil {
				return err
			}
			policy.Reviewers = nil
			if err = e.s.SetRoomChangePolicy(r.RoomID, authority.Actor{Role: authority.Owner}, policy); err != nil {
				return err
			}
		}
		return run(ctx, cmd)
	}
	err := e.s.Verdict(context.Background(), r.Key, namedReviewerID, r.Head, "approve", "Revoked during confirmation", 0)
	if !revoked || err == nil || e.record().Verdict != "" {
		t.Fatalf("revoked reviewer recorded a verdict: revoked=%v error=%v", revoked, err)
	}
}

func TestNamedReviewerApprovalKeepsDraftHold(t *testing.T) {
	e, r := namedReviewEnv(t)
	e.f.Draft = true
	e.write()
	if err := e.s.Verdict(context.Background(), r.Key, namedReviewerID, r.Head, "approve", "Reviewed while held", 0); err != nil {
		t.Fatal(err)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	items := MergeQueueSnapshot(st)
	if len(items) != 1 || items[0].State != "held" {
		t.Fatalf("named approval released the draft: %+v", items)
	}
}

func TestAnyAuthorizedReviewerApprovalCounts(t *testing.T) {
	e, r := namedReviewEnv(t)
	if err := e.s.Store.update(func(st *State) error {
		live := st.Records[r.Key]
		live.Reviewer = "pool-reviewer"
		live.Round, live.RoundHead = 1, live.Head
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Verdict(context.Background(), r.Key, namedReviewerID, r.Head, "changes", "Changes for this head", 0); err != nil {
		t.Fatal(err)
	}
	policy, _ := e.s.RoomChangePolicy(r.RoomID)
	policy.Review = "on"
	if err := e.s.SetRoomChangePolicy(r.RoomID, authority.Actor{Role: authority.Owner}, policy); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Verdict(context.Background(), r.Key, "pool-reviewer", r.Head, "approve", "Assigned review approves", 0); err != nil {
		t.Fatal(err)
	}
	other := "ses_20261017-000006-ffffffff"
	if err := e.s.Verdict(context.Background(), r.Key, other, r.Head, "approve", "Other named review approves", 0); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Verdict(context.Background(), r.Key, other, r.Head, "approve", "Other named review approves", 0); err != nil {
		t.Fatal(err)
	}
	got := e.record()
	if got.Verdict != "approve" || len(got.ReviewRounds) != 3 || got.ReviewRounds[0].Reviewer != namedReviewerID || got.ReviewRounds[1].Reviewer != "pool-reviewer" || got.ReviewRounds[2].Reviewer != other {
		t.Fatalf("authorized reviews were not kept once: %+v", got.ReviewRounds)
	}
}

func TestNamedReviewerCannotPromoteAnEarlierHeadApproval(t *testing.T) {
	e, r := namedReviewEnv(t)
	if err := e.s.Store.update(func(st *State) error {
		live := st.Records[r.Key]
		live.Round, live.RoundHead, live.Verdict = 1, "earlier-head", "approve"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Verdict(context.Background(), r.Key, namedReviewerID, r.Head, "changes", "Changes for the current head", 0); err != nil {
		t.Fatal(err)
	}
	if got := e.record(); got.Verdict != "changes" || got.RoundHead != r.Head {
		t.Fatalf("old approval moved to the new head: verdict=%s head=%s", got.Verdict, got.RoundHead)
	}
}

func TestNamedPendingVerdictReplaysWithoutAssignment(t *testing.T) {
	e, r := namedReviewEnv(t)
	scope := RateLimitScope{Host: r.Host, Account: "fixture", Resource: "core"}
	if err := e.s.Store.update(func(st *State) error {
		st.Records[r.Key].QuotaAccount = "fixture"
		st.RateLimits = map[string]RateLimit{scope.Key(): {PauseUntil: e.clock.Add(time.Hour)}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	err := e.s.Verdict(context.Background(), r.Key, namedReviewerID, r.Head, "approve", "Saved named review", 0)
	if _, ok := VerdictNotice(err); !ok {
		t.Fatalf("paused named review = %v", err)
	}
	if saved := e.record(); saved.PendingVerdict == nil || saved.Verdict != "" {
		t.Fatal("paused review was applied before confirmation")
	}
	if err := e.s.Store.update(func(st *State) error { st.RateLimits = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if got := e.record(); got.PendingVerdict != nil || got.Verdict != "approve" || got.Reviewer != "" || got.RoundHead != r.Head {
		t.Fatalf("confirmed named review did not apply: %+v", got)
	}
}

func TestNamedReviewFeedbackAfterApprovalIsDelivered(t *testing.T) {
	for _, verdict := range []state.ReviewVerdict{state.VerdictChanges} {
		t.Run(string(verdict), func(t *testing.T) {
			e, r := namedReviewEnv(t)
			if err := e.s.Verdict(t.Context(), r.Key, namedReviewerID, r.Head, "approve", "First approval", 0); err != nil {
				t.Fatal(err)
			}
			before := len(e.sent)
			if err := e.s.Verdict(t.Context(), r.Key, "ses_20261017-000006-ffffffff", r.Head, verdict, "Second reviewer feedback", 0); err != nil {
				t.Fatal(err)
			}
			if len(e.sent) != before+1 || e.sent[len(e.sent)-1].Target != r.Author || !strings.Contains(e.sent[len(e.sent)-1].Text, "Second reviewer feedback") {
				t.Fatalf("feedback was not delivered: %+v", e.sent[before:])
			}
			if e.record().Verdict != "approve" {
				t.Fatal("feedback erased the existing approval")
			}
		})
	}
}

func TestNamedReviewersDeliverBothChanges(t *testing.T) {
	e, r := namedReviewEnv(t)
	for i, actor := range []string{namedReviewerID, "ses_20261017-000006-ffffffff"} {
		summary := []string{"First changes", "Second changes"}[i]
		before := len(e.sent)
		if err := e.s.Verdict(t.Context(), r.Key, actor, r.Head, "changes", summary, 0); err != nil {
			t.Fatal(err)
		}
		if len(e.sent) != before+1 || e.sent[len(e.sent)-1].Target != r.Author || !strings.Contains(e.sent[len(e.sent)-1].Text, summary) {
			t.Fatalf("review %d was not delivered: %+v", i, e.sent[before:])
		}
	}
}

func pauseNamedReviews(t *testing.T, e *testEnv, r Record) {
	t.Helper()
	scope := RateLimitScope{Host: r.Host, Account: "fixture", Resource: "core"}
	if err := e.s.Store.update(func(st *State) error {
		st.Records[r.Key].QuotaAccount = "fixture"
		st.RateLimits = map[string]RateLimit{scope.Key(): {PauseUntil: e.clock.Add(time.Hour)}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestNamedPendingApprovalAfterChanges(t *testing.T) {
	e, r := namedReviewEnv(t)
	if err := e.s.Verdict(t.Context(), r.Key, namedReviewerID, r.Head, "changes", "First changes", 0); err != nil {
		t.Fatal(err)
	}
	pauseNamedReviews(t, e, r)
	err := e.s.Verdict(t.Context(), r.Key, "ses_20261017-000006-ffffffff", r.Head, "approve", "Other approval", 0)
	if _, ok := VerdictNotice(err); !ok {
		t.Fatalf("pending approval after changes = %v", err)
	}
	if err := e.s.Store.update(func(st *State) error { st.RateLimits = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if got := e.record(); got.PendingVerdict != nil || got.Verdict != "approve" || len(got.ReviewRounds) != 2 {
		t.Fatalf("pending approval did not apply: %+v", got)
	}
}

func TestTwoNamedPendingReviewersReplayIndependently(t *testing.T) {
	e, r := namedReviewEnv(t)
	pauseNamedReviews(t, e, r)
	for i, actor := range []string{namedReviewerID, "ses_20261017-000006-ffffffff"} {
		verdict := []state.ReviewVerdict{state.VerdictChanges, state.VerdictApprove}[i]
		err := e.s.Verdict(t.Context(), r.Key, actor, r.Head, verdict, actor+" review", 0)
		if _, ok := VerdictNotice(err); !ok {
			t.Fatalf("pending review %d = %v", i, err)
		}
		err = e.s.Verdict(t.Context(), r.Key, actor, r.Head, verdict, actor+" review", 0)
		if _, ok := VerdictNotice(err); !ok {
			t.Fatalf("duplicate pending review %d = %v", i, err)
		}
	}
	// Retrying the first reviewer still works after a second one was saved.
	if err := e.s.Verdict(t.Context(), r.Key, namedReviewerID, r.Head, "changes", namedReviewerID+" review", 0); err == nil {
		t.Fatal("pending retry was not reported")
	}
	saved := e.record()
	if saved.PendingVerdict == nil || saved.PendingVerdict.Next == nil || saved.PendingVerdict.Next.Next != nil {
		t.Fatal("pending reviews were not kept separately")
	}
	body, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	var reloaded Record
	if err = json.Unmarshal(body, &reloaded); err != nil {
		t.Fatal(err)
	}
	if err = e.s.Store.update(func(st *State) error {
		st.Records[r.Key].PendingVerdict = reloaded.PendingVerdict
		st.RateLimits = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if got := e.record(); got.PendingVerdict != nil || got.Verdict != "approve" || len(got.ReviewRounds) != 2 {
		t.Fatalf("pending reviewers did not apply once: %+v", got)
	}
}

func TestRevokedPendingReviewerDoesNotDropAnotherReview(t *testing.T) {
	e, r := namedReviewEnv(t)
	pauseNamedReviews(t, e, r)
	for _, actor := range []string{namedReviewerID, "ses_20261017-000006-ffffffff"} {
		err := e.s.Verdict(t.Context(), r.Key, actor, r.Head, "approve", actor+" approval", 0)
		if _, ok := VerdictNotice(err); !ok {
			t.Fatal(err)
		}
	}
	policy, _ := e.s.RoomChangePolicy(r.RoomID)
	policy.Reviewers = []string{"ses_20261017-000006-ffffffff"}
	if err := e.s.SetRoomChangePolicy(r.RoomID, authority.Actor{Role: authority.Owner}, policy); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Store.update(func(st *State) error { st.RateLimits = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	e.tick()
	got := e.record()
	if got.PendingVerdict != nil || got.Verdict != "approve" || len(got.ReviewRounds) != 1 || got.ReviewRounds[0].Reviewer != policy.Reviewers[0] {
		t.Fatalf("rejecting one reviewer lost another: %+v", got)
	}
}

func TestNamedReviewWithGeneratedFindingIDsIsIdempotent(t *testing.T) {
	e, r := namedReviewEnv(t)
	findings := []Finding{{Severity: "nit", File: "file.go", Line: 1, Why: "Use a clear name", Fix: "Rename it"}}
	for i := 0; i < 2; i++ {
		if err := e.s.VerdictWithFindings(t.Context(), r.Key, namedReviewerID, r.Head, "approve", "Reviewed with a nit", 0, findings); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.record(); len(got.ReviewRounds) != 1 || len(got.Findings) != 1 {
		t.Fatalf("retry duplicated the review: %+v", got)
	}
}

func TestPendingReplayConsumesReviewsWithInheritedFindingReplies(t *testing.T) {
	e, r := namedReviewEnv(t)
	finding := Finding{ID: "X", Severity: "nit", File: "file.go", Line: 1, Why: "Explain this case", Fix: "Add the explanation", Replies: []state.FindingReply{{Author: r.Author, Disposition: "fixed", Message: "Explained", Head: r.Head, At: e.clock}}}
	if err := e.s.Store.update(func(st *State) error { st.Records[r.Key].Findings = []Finding{finding}; return nil }); err != nil {
		t.Fatal(err)
	}
	pauseNamedReviews(t, e, r)
	err := e.s.Verdict(t.Context(), r.Key, namedReviewerID, r.Head, "changes", "First changes", 0)
	if _, ok := VerdictNotice(err); !ok {
		t.Fatal(err)
	}
	finding.Replies = nil
	err = e.s.VerdictWithFindings(t.Context(), r.Key, "ses_20261017-000006-ffffffff", r.Head, "changes", "Second review with X", 0, []Finding{finding})
	if _, ok := VerdictNotice(err); !ok {
		t.Fatal(err)
	}
	saved := e.record()
	if saved.PendingVerdict.Next == nil || len(saved.PendingVerdict.Next.Findings[0].Replies) != 1 {
		t.Fatal("pending review did not inherit the existing reply")
	}
	if err := e.s.Store.update(func(st *State) error { st.RateLimits = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if got := e.record(); got.PendingVerdict != nil || len(got.ReviewRounds) != 2 {
		t.Fatalf("replay kept a consumed entry: pending=%+v rounds=%d", got.PendingVerdict, len(got.ReviewRounds))
	}
	notices := len(e.sent)
	e.tick()
	if got := e.record(); got.PendingVerdict != nil || len(got.ReviewRounds) != 2 || len(e.sent) != notices {
		t.Fatal("a later tick replayed an already recorded review")
	}
}

func TestPendingComparisonIgnoresOnlyAuthorReplies(t *testing.T) {
	left := &state.PendingVerdict{Actor: "reviewer", Head: "head", Verdict: "changes", Summary: "Review", Findings: []Finding{{ID: "X", Severity: "nit", File: "file.go", Line: 1, Why: "Explain", Fix: "Add a comment", Replies: []state.FindingReply{{Message: "Done"}}}}}
	right := *left
	right.Findings = submissionFindings(left.Findings)
	if !samePendingVerdict(left, &right) {
		t.Fatal("inherited author replies changed the submission identity")
	}
	right.Findings[0].Fix = "A different request"
	if samePendingVerdict(left, &right) {
		t.Fatal("different reviewer input compared equal")
	}
}
