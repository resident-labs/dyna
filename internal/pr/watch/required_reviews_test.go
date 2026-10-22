package watch

import (
	"context"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/pr/facts"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

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
	items := MergeQueueSnapshot(snapshot)
	if len(items) != 1 || items[0].State != "required-reviews" || !reflect.DeepEqual(items[0].MissingRequiredReviewers, []string{safety}) || !strings.Contains(items[0].Reason, safety) {
		t.Fatalf("queue lost missing review: %+v", items)
	}
	if localMergeReady(e.record()) {
		t.Fatal("local publication bypasses required review")
	}
	if err := e.s.Verdict(t.Context(), r.Key, safety, "old", "approve", "Old head", 0); err == nil {
		t.Fatal("accepted stale required approval")
	}
	if err := e.s.Verdict(t.Context(), r.Key, safety, r.Head, "approve", "Safety reviewed", 0); err != nil {
		t.Fatal(err)
	}
	got := e.record()
	if len(got.ReviewRounds) != 2 || got.Reviewer != "" || len(e.launches) != 0 || !reflect.DeepEqual(got.RequiredReviewers, []string{namedReviewerID, safety}) || len(facts.MissingRequiredReviewers(got)) != 0 || !localMergeReady(got) {
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
	items = MergeQueueSnapshot(snapshot)
	if len(items) != 1 || items[0].State != "ready" || len(items[0].MissingRequiredReviewers) != 0 {
		t.Fatalf("queue did not count both required approvals: %+v", items)
	}
	// A real remote push resets approvals while keeping both requirements,
	// even though the automatic review pool remains off.
	e.f.Pull.Head.SHA = strings.Repeat("b", 40)
	e.tick()
	got = e.record()
	if got.Head != e.f.Pull.Head.SHA || !reflect.DeepEqual(got.RequiredReviewers, []string{namedReviewerID, safety}) || !reflect.DeepEqual(facts.MissingRequiredReviewers(got), got.RequiredReviewers) || localMergeReady(got) {
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

// A requirement cannot land while a publisher validates or publishes a batch:
// both take the merge publication lock.
func TestRequiredReviewWriteIsRefusedWhilePublicationLockHeld(t *testing.T) {
	e, r := namedReviewEnv(t)
	unlock, err := e.s.mergeLock()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.s.RequireReviewers(r.Key, r.Author, r.RoomID, []string{namedReviewerID}); err == nil {
		t.Fatal("requirement landed while a publisher held the lock")
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
		got := MergeQueueSnapshot(st)
		if len(got) != 1 || got[0].State != "required-reviews" || !reflect.DeepEqual(got[0].MissingRequiredReviewers, []string{"required"}) {
			t.Fatalf("%s queue: %+v", kind, got)
		}
		if facts.Ready(r, facts.FromRecord(r)) || localMergeReady(r) {
			t.Fatalf("%s publication was ready", kind)
		}
	}
}
