package watch

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/pr/facts"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

// shortenRequirementWait keeps a refused requirement write fast in tests.
func shortenRequirementWait(t *testing.T, wait time.Duration) {
	t.Helper()
	previous := requirementLockWait
	requirementLockWait = wait
	t.Cleanup(func() { requirementLockWait = previous })
}

// A head move resets required approvals, so each required reviewer must be
// re-assigned at the new head. Without the notice a dormant named reviewer
// never learns that its older verdict stopped counting, and the PR stalls
// until someone wakes the reviewer by hand (D-64).
func TestNewHeadReNotifiesDormantRequiredReviewers(t *testing.T) {
	e, r := namedReviewEnv(t)
	const safety = "ses_20261017-000007-1234abcd"
	recordtest.Write(t, e.s.sessionStore().Root, safety, subagent.Meta{ID: safety, Name: "safety", RoomID: r.RoomID, Room: e.author.Room})
	if err := e.s.RequireReviewers(r.Key, r.Author, r.RoomID, []string{safety}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Verdict(t.Context(), r.Key, safety, r.Head, "approve", "Safety reviewed", 0); err != nil {
		t.Fatal(err)
	}
	// A remote push moves the head; the pool stays off, so the required
	// reviewer is the only session that can unblock the change.
	e.f.Pull.Head.SHA = strings.Repeat("b", 40)
	e.tick()
	newHead := e.record().Head
	if newHead != e.f.Pull.Head.SHA || facts.MissingRequiredReviewers(e.record())[0] != safety {
		t.Fatal("setup did not move the head and reset the approval")
	}
	var assignment *Notice
	for i := range e.sent {
		n := e.sent[i]
		if n.Target == safety && strings.Contains(n.Text, "Head: "+newHead) {
			assignment = &e.sent[i]
		}
	}
	if assignment == nil {
		t.Fatal("head move did not re-notify the required reviewer")
	}
	if !assignment.System || assignment.Type != room.PRReviewAssigned || !strings.Contains(assignment.Text, "Review ") || !strings.Contains(assignment.Text, newHead) {
		t.Fatalf("re-assignment was not a typed waking review notice: %+v", assignment)
	}
	// A further push retires the first re-assignment: only the newest head
	// asks, so the reviewer never acts on a stale head.
	e.f.Pull.Head.SHA = strings.Repeat("c", 40)
	e.tick()
	newest := e.record().Head
	forHead := map[string]int{}
	for _, n := range e.sent {
		if n.Target == safety && strings.Contains(n.Text, "Head: ") {
			forHead[strings.SplitN(strings.SplitN(n.Text, "Head: ", 2)[1], "\n", 2)[0]]++
		}
	}
	if forHead[newest] != 1 || forHead[newHead] != 1 {
		t.Fatalf("each head must re-assign the reviewer exactly once: %v", forHead)
	}
	// The re-notified reviewer closes the loop at the current head.
	if err := e.s.Verdict(t.Context(), r.Key, safety, newest, "approve", "Safety reviewed the new head", 0); err != nil {
		t.Fatal(err)
	}
	if missing := facts.MissingRequiredReviewers(e.record()); len(missing) != 0 {
		t.Fatalf("approval at the re-assigned head still missing: %v", missing)
	}
}

// A head that leaves and returns must restore its still-undelivered
// assignment: the head-only notice ID would otherwise stay retired and the
// reviewer waits for an approval nobody asked for (PR #659 review).
// A draft holds required-review assignments exactly as it holds the pool,
// and the release delivers them without another push (PR #659 review).
func TestDraftHoldsRequiredReviewAssignmentsUntilRelease(t *testing.T) {
	e, r := namedReviewEnv(t)
	const safety = "ses_20261017-000007-1234abcd"
	recordtest.Write(t, e.s.sessionStore().Root, safety, subagent.Meta{ID: safety, Name: "safety", RoomID: r.RoomID, Room: e.author.Room})
	if err := e.s.RequireReviewers(r.Key, r.Author, r.RoomID, []string{safety}); err != nil {
		t.Fatal(err)
	}
	// A push on a held draft PR assigns nobody.
	e.f.Draft = true
	e.f.Pull.Head.SHA = strings.Repeat("b", 40)
	e.tick()
	if got := e.record(); !got.IsDraft {
		t.Fatal("setup did not hold the PR as a draft")
	}
	for _, n := range e.sent {
		if n.Target == safety && n.Kind == "required-review" {
			t.Fatalf("draft push delivered a required review assignment: %+v", n)
		}
	}
	// The draft releases with no push of its own: the held assignment is
	// queued and delivered at the current head.
	e.f.Draft = false
	e.tick()
	var released *Notice
	for i := range e.sent {
		if e.sent[i].Target == safety && e.sent[i].Kind == "required-review" {
			copy := e.sent[i]
			released = &copy
		}
	}
	if released == nil || !strings.Contains(released.Text, "Head: "+e.record().Head) {
		t.Fatalf("draft release did not deliver the held assignment: %+v", released)
	}
	// The reviewer closes the loop at that head.
	if err := e.s.Verdict(t.Context(), r.Key, safety, e.record().Head, "approve", "Safety reviewed after the release", 0); err != nil {
		t.Fatal(err)
	}
	if missing := facts.MissingRequiredReviewers(e.record()); len(missing) != 0 {
		t.Fatalf("approval after the release still missing: %v", missing)
	}
}

func TestRequiredReviewAssignmentReturnsWhenHeadReturns(t *testing.T) {
	e, r := namedReviewEnv(t)
	const safety = "ses_20261017-000007-1234abcd"
	recordtest.Write(t, e.s.sessionStore().Root, safety, subagent.Meta{ID: safety, Name: "safety", RoomID: r.RoomID, Room: e.author.Room})
	if err := e.s.RequireReviewers(r.Key, r.Author, r.RoomID, []string{safety}); err != nil {
		t.Fatal(err)
	}
	const headB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const headC = "cccccccccccccccccccccccccccccccccccccccc"
	// Delivery fails while the head is B, so B's assignment is still pending
	// when C moves the head away and retires it.
	failing := true
	e.s.Deliver = func(_ context.Context, _ Record, n Notice) (string, error) {
		if failing {
			return "", errors.New("room post unavailable")
		}
		e.sent = append(e.sent, n)
		return n.ID, nil
	}
	tickIgnoringErrors := func() {
		e.t.Helper()
		e.clock = e.clock.Add(time.Minute)
		e.write()
		_, _ = e.s.Tick(context.Background())
	}
	e.f.Pull.Head.SHA = headB
	tickIgnoringErrors()
	if got := e.record(); !slices.ContainsFunc(got.Notices, func(n Notice) bool {
		return n.Target == safety && n.Kind == "required-review" && !n.Done && n.SupersededBy == ""
	}) {
		t.Fatal("setup did not queue B's pending assignment")
	}
	failing = false
	e.f.Pull.Head.SHA = headC
	e.tick()
	if got := e.record(); !slices.ContainsFunc(got.Notices, func(n Notice) bool {
		return n.Target == safety && n.Kind == "required-review" && (n.SupersededBy == "new-head:"+headC || n.SupersededBy == "head-changed:"+headC)
	}) {
		t.Fatalf("C's move did not retire B's assignment: %+v", got.Notices)
	}
	// The head returns to B: the retired assignment must be revived and
	// delivered, because the reviewer still owes B's approval.
	e.f.Pull.Head.SHA = headB
	e.tick()
	var revived *Notice
	for i := range e.record().Notices {
		n := e.record().Notices[i]
		if n.Target == safety && n.Kind == "required-review" && strings.Contains(n.Text, "Head: "+headB) {
			copy := e.record().Notices[i]
			revived = &copy
		}
	}
	if revived == nil || !revived.Done || revived.SupersededBy != "" || revived.PostID == "" {
		t.Fatalf("returning head did not revive and deliver B's assignment: %+v", revived)
	}
	for _, n := range e.sent {
		if n.Target == safety && strings.Contains(n.Text, "Head: "+headB) {
			return
		}
	}
	t.Fatal("the revived B assignment was never delivered")
}

func TestRequiredReviewersUseExistingUnassignedVerdictPath(t *testing.T) {
	e, r := namedReviewEnv(t)
	const safety = "ses_20261017-000007-1234abcd"
	recordtest.Write(t, e.s.sessionStore().Root, safety, subagent.Meta{ID: safety, Name: "safety", RoomID: r.RoomID, Room: e.author.Room})
	if err := e.s.RequireReviewers(r.Key, r.Author, r.RoomID, []string{namedReviewerID, safety, namedReviewerID}); err != nil {
		t.Fatal(err)
	}
	// The added reviewer is not in the room roster and has no pool assignment.
	if err := e.s.Verdict(t.Context(), r.Key, namedReviewerID, r.Head, "approve", "Architecture reviewed", 0); err != nil {
		t.Fatal(err)
	}
	snapshot, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	items := MergeQueueSnapshot(snapshot, nil)
	if len(items) != 1 || items[0].State != "required-reviews" || !reflect.DeepEqual(items[0].MissingRequiredReviewers, []string{safety}) || !strings.Contains(items[0].Reason, safety) {
		t.Fatalf("queue lost missing review: %+v", items)
	}
	if localMergeReady(e.record(), nil) {
		t.Fatal("local publication bypasses required review")
	}
	if err := e.s.Verdict(t.Context(), r.Key, safety, "old", "approve", "Old head", 0); err == nil {
		t.Fatal("accepted stale required approval")
	}
	if err := e.s.Verdict(t.Context(), r.Key, safety, r.Head, "approve", "Safety reviewed", 0); err != nil {
		t.Fatal(err)
	}
	got := e.record()
	if len(got.ReviewRounds) != 2 || got.Reviewer != "" || len(e.launches) != 0 || !reflect.DeepEqual(got.RequiredReviewers, []string{namedReviewerID, safety}) || len(facts.MissingRequiredReviewers(got)) != 0 || !localMergeReady(got, nil) {
		t.Fatalf("required verdict did not use shared history: %+v", got)
	}
	if err := e.s.Store.update(func(st *State) error {
		st.RepositoryFacts = map[string]state.RepositoryFacts{defaultRepo(got): {DefaultBranch: "main", MergeTrialCI: true, CheckedAt: e.clock}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err = e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	items = MergeQueueSnapshot(snapshot, nil)
	if len(items) != 1 || items[0].State != "ready" || len(items[0].MissingRequiredReviewers) != 0 {
		t.Fatalf("queue did not count both required approvals: %+v", items)
	}
	// A real remote push resets approvals while keeping both requirements,
	// even though the automatic review pool remains off.
	e.f.Pull.Head.SHA = strings.Repeat("b", 40)
	e.tick()
	got = e.record()
	if got.Head != e.f.Pull.Head.SHA || !reflect.DeepEqual(got.RequiredReviewers, []string{namedReviewerID, safety}) || !reflect.DeepEqual(facts.MissingRequiredReviewers(got), got.RequiredReviewers) || localMergeReady(got, nil) {
		t.Fatal("remote push did not reset required approvals and preserve requirements")
	}
}

func TestRequiredReviewersAddAtomicallyAndBindCheckedAuthor(t *testing.T) {
	e, r := namedReviewEnv(t)
	const other = "ses_20261017-000006-ffffffff"
	if err := e.s.RequireReviewers(r.Key, r.Author, r.RoomID, []string{namedReviewerID}); err != nil {
		t.Fatal(err)
	}
	before, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, reviewers := range [][]string{nil, {other, "missing-session"}, {r.Author}} {
		if err := e.s.RequireReviewers(r.Key, r.Author, r.RoomID, reviewers); err == nil {
			t.Fatalf("accepted invalid requirement %v", reviewers)
		}
	}
	if err := e.s.RequireReviewers(r.Key, "changed-author", r.RoomID, []string{other}); err == nil {
		t.Fatal("accepted changed author")
	}
	if err := e.s.RequireReviewers(r.Key, r.Author, "other-room", []string{other}); err == nil {
		t.Fatal("accepted changed room")
	}
	if err := e.s.RequireReviewers(r.Key, r.Author, r.RoomID, []string{namedReviewerID}); err != nil {
		t.Fatal(err)
	}
	after, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("refused or duplicate requirement changed state")
	}
	if err := e.s.RequireReviewers(r.Key, r.Author, r.RoomID, []string{other}); err != nil {
		t.Fatal(err)
	}
	if got := e.record().RequiredReviewers; !reflect.DeepEqual(got, []string{namedReviewerID, other}) {
		t.Fatalf("addition removed earlier requirement: %v", got)
	}
	// Canonical session validation also rejects closed or moved reviewers.
	meta, err := e.s.sessionStore().ReadMeta(other)
	if err != nil {
		t.Fatal(err)
	}
	meta.RoomID = "other-room"
	recordtest.Write(t, e.s.sessionStore().Root, other, meta)
	if err := e.s.RequireReviewers(r.Key, r.Author, r.RoomID, []string{other}); err == nil {
		t.Fatal("accepted moved reviewer")
	}
	meta.RoomID = r.RoomID
	meta.Closed = e.clock
	recordtest.Write(t, e.s.sessionStore().Root, other, meta)
	if err := e.s.RequireReviewers(r.Key, r.Author, r.RoomID, []string{other}); err == nil {
		t.Fatal("accepted closed reviewer")
	}
}

// A merge publisher holds merge-publish.lock for whole ticks, and trials run
// for minutes, so a requirement write must not wait behind that lock (D-409).
// Only the publisher's short final check-and-publish step may hold it back.
func TestRequiredReviewWriteDoesNotWaitForTheMergePublisher(t *testing.T) {
	e, r := namedReviewEnv(t)
	unlock, err := e.s.mergeLock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := e.s.RequireReviewers(r.Key, r.Author, r.RoomID, []string{namedReviewerID}); err != nil {
		t.Fatalf("requirement waited behind the merge publisher: %v", err)
	}
	if got := e.record().RequiredReviewers; !reflect.DeepEqual(got, []string{namedReviewerID}) {
		t.Fatalf("requirement did not land: %v", got)
	}
}

// The final check-and-publish step still excludes a requirement write: one
// that arrives inside it waits, and is refused only when that step outlives
// the bounded wait, so no batch publishes with a requirement it was not
// checked against.
func TestRequiredReviewWriteWaitsOutAPublishersFinalStep(t *testing.T) {
	e, r := namedReviewEnv(t)
	shortenRequirementWait(t, 40*time.Millisecond)
	// Separate opens contend within one process, so this stands in for a
	// publisher holding the gate across its final step.
	unlock, err := e.s.requirementLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.s.RequireReviewers(r.Key, r.Author, r.RoomID, []string{namedReviewerID}); err == nil {
		t.Fatal("requirement landed inside a publisher's final step")
	}
	if got := e.record().RequiredReviewers; len(got) != 0 {
		t.Fatalf("refused write changed state: %v", got)
	}
	unlock()
	if err := e.s.RequireReviewers(r.Key, r.Author, r.RoomID, []string{namedReviewerID}); err != nil {
		t.Fatal(err)
	}
}

// A requirement added by a hook during MergeFix publication is refused, so the
// batch never publishes with a requirement it was not checked against.
func TestRequiredReviewAddedDuringPublicationIsRefused(t *testing.T) {
	shortenRequirementWait(t, 40*time.Millisecond)
	e, fs, _ := mergeEnv(t, 1, 2)
	fs[2].Pull.Base.Ref = fs[1].Pull.Head.Ref
	const key = "github.com/owner/repo#1"
	recordtest.Write(t, e.s.sessionStore().Root, namedReviewerID, subagent.Meta{ID: namedReviewerID, Name: "architecture", RoomID: e.author.RoomID, Room: e.author.Room})
	var refused error
	run := e.s.GH.run
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		if strings.Contains(strings.Join(cmd.Args, " "), "pr edit ") && refused == nil {
			r, err := e.s.Store.Get(key)
			if err != nil {
				return err
			}
			refused = e.s.RequireReviewers(key, r.Author, r.RoomID, []string{namedReviewerID})
		}
		return run(ctx, cmd)
	}
	_ = e.s.MergeFix(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), key)
	if refused == nil {
		t.Fatal("requirement landed during publication")
	}
	r, err := e.s.Store.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.RequiredReviewers) != 0 {
		t.Fatalf("refused write changed state: %v", r.RequiredReviewers)
	}
}

// Ordinary and local queue display use the same derived missing IDs.
func TestRequiredReviewsBlockLocalAndGitHubQueueEntries(t *testing.T) {
	yes := true
	for _, kind := range []string{"github", "local"} {
		r := Record{Key: "pr", Kind: kind, Repo: "owner/repo", Host: "github.com", RoomID: "room", Watching: true, State: "open", Head: "h", RoundHead: "h", Verdict: "approve", CI: "passed", BaseRef: "main", DefaultBranch: "main", Mergeable: &yes, RequiredReviewers: []string{"required"}}
		st := State{Records: map[string]*Record{r.Key: &r}, RepositoryFacts: map[string]state.RepositoryFacts{defaultRepo(r): {MergeTrialCI: true}}}
		got := MergeQueueSnapshot(st, nil)
		if len(got) != 1 || got[0].State != "required-reviews" || !reflect.DeepEqual(got[0].MissingRequiredReviewers, []string{"required"}) {
			t.Fatalf("%s queue: %+v", kind, got)
		}
		if facts.Ready(r, facts.FromRecord(r), nil) || localMergeReady(r, nil) {
			t.Fatalf("%s publication was ready", kind)
		}
	}
}
