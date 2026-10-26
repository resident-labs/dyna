package watch

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/subagent"
)

func changeGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null", "-c", "user.name=Test", "-c", "user.email=test@example.test", "-c", "commit.gpgSign=false"}, args...)...)
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s %v", args, data, err)
	}
	return strings.TrimSpace(string(data))
}
func writeWatchDefinition(t *testing.T, dir string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "..", ".dyna", "local-ci.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".dyna", "local-ci.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func changeRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	changeGit(t, dir, "init", "-b", "main")
	writeWatchDefinition(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "code.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changeGit(t, dir, "add", ".")
	changeGit(t, dir, "commit", "-m", "base")
	return dir, changeGit(t, dir, "rev-parse", "HEAD")
}
func changeCommit(t *testing.T, dir, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "code.txt"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	changeGit(t, dir, "add", ".")
	changeGit(t, dir, "commit", "-m", "update")
	return changeGit(t, dir, "rev-parse", "HEAD")
}
func localChange(t *testing.T, e *testEnv) (Record, string) {
	t.Helper()
	dir, _ := changeRepo(t)
	changeGit(t, dir, "checkout", "-b", "feature")
	changeCommit(t, dir, "feature\n")
	e.author.Dir = dir
	r, created, err := e.s.OpenChange(context.Background(), authority.Actor{Role: authority.Owner}, e.author, OpenChangeOptions{Dir: dir, Base: "main", Notes: ChangeNotes{Intent: "Fix parser", Testing: "unit tests"}, Category: "fix"})
	if err != nil || !created {
		t.Fatalf("open change: %+v %v", r, err)
	}
	return r, dir
}

func TestLocalChangeUsesDefaultBranchAndExplicitBase(t *testing.T) {
	e := newEnv(t)
	dir, base := changeRepo(t)
	changeGit(t, dir, "branch", "-M", "trunk")
	changeGit(t, dir, "update-ref", "refs/remotes/origin/trunk", base)
	changeGit(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
	changeGit(t, dir, "checkout", "-b", "feature")
	changeCommit(t, dir, "feature\n")
	e.author.Dir = dir
	r, created, err := e.s.OpenChange(context.Background(), authority.Actor{Role: authority.Owner}, e.author, OpenChangeOptions{Dir: dir})
	if err != nil || !created || r.Base != base || r.BaseRef != "origin/trunk" {
		t.Fatalf("default base: %+v, created %t, %v", r, created, err)
	}
	explicit, created, err := e.s.OpenChange(context.Background(), authority.Actor{Role: authority.Owner}, e.author, OpenChangeOptions{Dir: dir, Base: "trunk"})
	if err != nil || created || explicit.Base != base || explicit.BaseRef != "trunk" {
		t.Fatalf("explicit base: %+v, created %t, %v", explicit, created, err)
	}
}

func TestLocalChangeOpenQueueHeadReplacementAndClose(t *testing.T) {
	e := newEnv(t)
	r, dir := localChange(t, e)
	if r.Kind != "local" || r.ChangeID == "" || r.Number != 0 || r.Branch != "feature" || r.Notes.Intent != "Fix parser" {
		t.Fatalf("local record: %+v", r)
	}
	e.s.GH.Executable = "/must-not-contact-github"
	e.tick()
	assigned, err := e.s.Store.Get(r.ChangeID)
	if err != nil || assigned.Reviewer == "" || assigned.Round != 1 {
		t.Fatalf("local assignment: %+v %v", assigned, err)
	}
	if len(e.launches) != 1 || !strings.Contains(e.launches[0], "dyna git change verdict") {
		t.Fatalf("local task: %+v", e.launches)
	}
	head := changeCommit(t, dir, "updated\n")
	e.tick()
	next, _ := e.s.Store.Get(r.ChangeID)
	if next.Head != head || next.Round != 2 || next.Reviewer == assigned.Reviewer || next.Reviewer == "" || len(e.launches) != 2 {
		t.Fatalf("local push: %+v", next)
	}
	again, created, err := e.s.OpenChange(context.Background(), authority.Actor{Role: authority.Owner}, e.author, OpenChangeOptions{Dir: dir})
	if err != nil || created || again.ChangeID != r.ChangeID || again.Notes.Intent != "Fix parser" {
		t.Fatalf("idempotent open: %+v %v", again, err)
	}
	if err = e.s.CloseChange(r.ChangeID, r.Author); err != nil {
		t.Fatal(err)
	}
	closed, _ := e.s.Store.Get(r.ChangeID)
	if closed.Watching || closed.State != "closed" {
		t.Fatal("local close did not stop tracking")
	}
}

// Closing a change closes its reviewer for the engine, not for a caller: the
// engine refuses a close that names no caller (D-206).
func TestClosingAChangeClosesItsReviewerAsTheEngine(t *testing.T) {
	e := newEnv(t)
	r, _ := localChange(t, e)
	e.tick()
	assigned, _ := e.s.Store.Get(r.ChangeID)
	if assigned.Reviewer == "" {
		t.Fatalf("no reviewer assigned: %+v", assigned)
	}
	closeFake := e.s.CloseReviewer
	var closeErr error
	e.s.CloseReviewer = func(ctx context.Context, id string) error {
		if closeErr = authority.Check("subagent close", authority.Of(ctx), nil); closeErr != nil {
			return closeErr
		}
		return closeFake(ctx, id)
	}
	t.Cleanup(authority.InEngine())
	if err := e.s.CloseChange(r.ChangeID, r.Author); err != nil {
		t.Fatal(err)
	}
	if closeErr != nil || e.reviewers[assigned.Reviewer].Status != subagent.StatusClosed {
		t.Fatalf("reviewer close = %v, status %v; want it closed", closeErr, e.reviewers[assigned.Reviewer].Status)
	}
}

func TestLocalChangeRejectsDirtyAndDetachedWorktrees(t *testing.T) {
	e := newEnv(t)
	_, dir := localChange(t, e)
	if err := os.WriteFile(filepath.Join(dir, "dirty"), []byte("dirty"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.s.OpenChange(context.Background(), authority.Actor{Role: authority.Owner}, e.author, OpenChangeOptions{Dir: dir}); err == nil || !strings.Contains(err.Error(), "commit") {
		t.Fatalf("dirty open: %v", err)
	}
	os.Remove(filepath.Join(dir, "dirty"))
	changeGit(t, dir, "checkout", "--detach")
	if _, _, err := e.s.OpenChange(context.Background(), authority.Actor{Role: authority.Owner}, e.author, OpenChangeOptions{Dir: dir}); err == nil || !strings.Contains(err.Error(), "named branch") {
		t.Fatalf("detached open: %v", err)
	}
}
func TestFindingsRepliesAndIncrementalReview(t *testing.T) {
	e := newEnv(t)
	r, dir := localChange(t, e)
	e.tick()
	r, _ = e.s.Store.Get(r.Key)
	findings := []Finding{{Severity: "blocking", File: "code.txt", Line: 1, Why: "Missing bounds check", Fix: "Check the index"}}
	if err := e.s.VerdictWithFindings(context.Background(), r.Key, r.Reviewer, r.Head, "changes", "Fix bounds", 0, findings); err != nil {
		t.Fatal(err)
	}
	reviewed, _ := e.s.Store.Get(r.Key)
	if len(reviewed.ReviewRounds) != 1 || len(reviewed.Findings) != 1 || reviewed.Findings[0].ID == "" {
		t.Fatalf("findings: %+v", reviewed)
	}
	id := reviewed.Findings[0].ID
	if err := e.s.ReplyFinding(context.Background(), r.Key, r.Author, e.author.ID, id, "question", "Which index?"); err != nil {
		t.Fatal(err)
	}
	e.tick()
	again, _ := e.s.Store.Get(r.Key)
	if again.Round != 2 || again.Verdict != "" || len(again.Findings[0].Replies) != 1 || len(again.ReviewRounds[0].Findings[0].Replies) != 1 {
		t.Fatalf("reply did not start another round: %+v", again)
	}
	// A later head gets delta context and retains earlier replies for verification.
	head := changeCommit(t, dir, "bounds checked\n")
	e.tick()
	again, _ = e.s.Store.Get(r.Key)
	task := ""
	texts := append([]string(nil), e.launches...)
	for _, n := range e.sent {
		texts = append(texts, n.Text)
	}
	for _, text := range texts {
		if strings.Contains(text, "The current head is "+head) {
			task = text
		}
	}
	if !strings.Contains(task, "You last saw head "+r.Head) || !strings.Contains(task, "Never run the test suite") {
		t.Fatalf("incremental task: %s", task)
	}
	paths, err := filepath.Glob(filepath.Join(e.s.Store.Root, "review-inputs", "*", "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatal("review input missing")
	}
	var input ReviewContext
	for _, path := range paths {
		data, _ := os.ReadFile(path)
		var candidate ReviewContext
		if json.Unmarshal(data, &candidate) == nil && candidate.Assignment.Head == head {
			input = candidate
		}
	}
	if input.Previous == nil || input.DeltaPath == "" || len(input.Findings[0].Replies) != 1 {
		t.Fatalf("incremental context: %+v", input)
	}
	data, err := os.ReadFile(input.DeltaPath)
	if err != nil || !strings.Contains(string(data), "bounds checked") {
		t.Fatalf("delta: %s %v", data, err)
	}
	if err = e.s.VerdictWithFindings(context.Background(), r.Key, again.Reviewer, head, state.VerdictApprove, "Ready with a naming nit", 0, []Finding{{Severity: state.FindingNit, File: "code.txt", Why: "Name is unclear", Fix: "Rename it"}}); err != nil {
		t.Fatal(err)
	}
	approvedRecord, _ := e.s.Store.Get(r.Key)
	if len(approvedRecord.ReviewRounds) != 2 || approvedRecord.Verdict != state.VerdictApprove || len(approvedRecord.Findings) != 1 || approvedRecord.Findings[0].Severity != state.FindingNit {
		t.Fatalf("final review: %+v", approvedRecord)
	}
}
func TestFindingsValidationAndStaleLocalVerdict(t *testing.T) {
	e := newEnv(t)
	r, dir := localChange(t, e)
	e.tick()
	r, _ = e.s.Store.Get(r.Key)
	finding := Finding{Severity: "blocking", File: "code.txt", Why: "Bad", Fix: "Fix it"}
	if err := e.s.VerdictWithFindings(context.Background(), r.Key, r.Reviewer, r.Head, "approve", "Ready", 0, []Finding{finding}); err == nil {
		t.Fatal("blocking finding approved")
	}
	for _, invalid := range []Finding{{Severity: "unknown", File: "code.txt", Why: "Bad", Fix: "Fix"}, {Severity: "nit", File: "code.txt", Line: -1, Why: "Bad", Fix: "Fix"}, {ID: "bad id", Severity: "nit", File: "code.txt", Why: "Bad", Fix: "Fix"}} {
		if _, err := prepareFindings(r, "changes", []Finding{invalid}); err == nil {
			t.Fatalf("invalid finding accepted: %+v", invalid)
		}
	}
	changeCommit(t, dir, "new unreviewed head\n")
	var stale *ErrStaleVerdict
	if err := e.s.Verdict(context.Background(), r.Key, r.Reviewer, r.Head, "approve", "Stale", 0); !errors.As(err, &stale) {
		t.Fatalf("stale local verdict: %v", err)
	}
}
func TestFindingsRetainStableIDsAndAuthorReplies(t *testing.T) {
	reply := state.FindingReply{Disposition: "fixed", Message: "fixed", Head: "head", At: time.Now()}
	r := Record{Round: 2, Findings: []Finding{{ID: "bounds", Replies: []state.FindingReply{reply}}}}
	prepared, err := prepareFindings(r, "changes", []Finding{{ID: "bounds", Severity: "should-fix", File: "code.txt", Why: "Still wrong", Fix: "Try this", Replies: []state.FindingReply{{Message: "forged"}}}})
	if err != nil || len(prepared[0].Replies) != 1 || prepared[0].Replies[0].Message != "fixed" {
		t.Fatalf("reply provenance: %+v %v", prepared, err)
	}
}

func TestPRTrackingAttachesToLocalChangeAndPreservesIdentity(t *testing.T) {
	e := newEnv(t)
	local, dir := localChange(t, e)
	changeGit(t, dir, "config", "remote.origin.url", "https://github.com/owner/repo.git")
	e.f.Pull.Head.Ref = local.Branch
	e.f.Pull.Head.SHA = local.Head
	e.f.Pull.Base.Ref = "main"
	e.f.Pull.Base.SHA = local.Base
	e.write()
	tracked, created, err := e.s.Track(context.Background(), authority.Actor{Role: authority.Owner}, Ref{Host: "github.com", Repo: "owner/repo", Number: 17}, e.author, dir)
	if err != nil || !created || tracked.Key == local.Key || tracked.ChangeID != local.ChangeID || tracked.Kind != "github" || tracked.Notes.Intent != local.Notes.Intent {
		t.Fatalf("PR attach: %+v %v", tracked, err)
	}
	same, err := e.s.Store.Get(local.ChangeID)
	if err != nil || same.Key != tracked.Key {
		t.Fatalf("stable change ID: %+v %v", same, err)
	}
	records, err := e.s.Changes(local.RoomID)
	if err != nil || len(records) != 1 {
		t.Fatalf("duplicate changes after attach: %+v %v", records, err)
	}
}

func TestLocalChangeUsesLocalPublishingInGitHubRoom(t *testing.T) {
	e := newEnv(t)
	r, _ := localChange(t, e)
	if err := e.s.ConfigurePublishing(r.RoomID, PublishingGitHub); err != nil {
		t.Fatal(err)
	}
	e.tick()
	assigned, _ := e.s.Store.Get(r.Key)
	if assigned.ReviewPublishing != PublishingLocal || !strings.Contains(e.launches[0], "Dyna-only") {
		t.Fatalf("local publishing: %+v", assigned)
	}
	if err := e.s.Verdict(context.Background(), r.Key, assigned.Reviewer, assigned.Head, "approve", "Local change is ready", 0); err != nil {
		t.Fatalf("local verdict required a GitHub review: %v", err)
	}
}
func TestPRTrackingCannotTakeAnotherAuthorsLocalBranch(t *testing.T) {
	for _, otherRoom := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-room", true: "other-room"}[otherRoom], func(t *testing.T) {
			e := newEnv(t)
			local, dir := localChange(t, e)
			changeGit(t, dir, "config", "remote.origin.url", "https://github.com/owner/repo.git")
			e.f.Pull.Head.Ref = local.Branch
			e.f.Pull.Head.SHA = local.Head
			e.write()
			other := transferTarget(e)
			if otherRoom {
				other.RoomID = "another-room"
			}
			if _, _, err := e.s.Track(context.Background(), authority.Actor{Role: authority.Owner}, Ref{Host: "github.com", Repo: "owner/repo", Number: 17}, other, dir); err == nil || !strings.Contains(err.Error(), "another room or author") {
				t.Fatalf("ownership bypass: %v", err)
			}
			records, err := e.s.Changes("")
			if err != nil || len(records) != 1 || records[0].Author != e.author.ID {
				t.Fatalf("duplicate or reassigned change: %+v %v", records, err)
			}
		})
	}
}

// pr/view checks the author outside the PR lock; a write refuses when the
// author changed after that check, such as after a transfer in between.
func TestAuthorWritesRefuseAChangedAuthor(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	for name, write := range map[string]func() error{
		"close": func() error { return e.s.CloseChange(r.Key, "ses_20261017-000009-99999999") },
		"override": func() error {
			return e.s.SetChangePolicyOverride(r.Key, "ses_20261017-000009-99999999", PolicyOverride{Review: "off", Reason: "x"})
		},
		"notes": func() error {
			return e.s.UpdateChangeNotes(r.Key, "ses_20261017-000009-99999999", ChangeNotes{Intent: "x"}, "", nil)
		},
		"reply": func() error {
			return e.s.ReplyFinding(t.Context(), r.Key, "ses_20261017-000009-99999999", "", "f1", "fixed", "x")
		},
	} {
		if err := write(); err == nil || !strings.Contains(err.Error(), "author changed") {
			t.Errorf("%s by a stale author = %v; want a refusal", name, err)
		}
	}
	if got := e.record(); got.Watching != true || got.Notes != r.Notes {
		t.Fatalf("a refused write changed the record: %+v", got)
	}
}

func TestUpdatingOneNoteKeepsTheOthers(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	full := ChangeNotes{Intent: "why", Testing: "old", Risks: "risk", Questions: "ask"}
	if err := e.s.UpdateChangeNotes(r.Key, r.Author, full, "fix", []string{"dep"}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.UpdateChangeNotes(r.Key, r.Author, ChangeNotes{Testing: "new"}, "", nil); err != nil {
		t.Fatal(err)
	}
	want := full
	want.Testing = "new"
	got := e.record()
	if got.Notes != want || got.Category != "fix" || len(got.Dependencies) != 1 {
		t.Fatalf("update with only testing = %+v, %q, %v; want notes %+v and category and dependencies kept", got.Notes, got.Category, got.Dependencies, want)
	}
}

func TestUpdatedNotesThatTogetherExceedTheLimitAreRefused(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	first := ChangeNotes{Intent: strings.Repeat("a", 8000)}
	if err := e.s.UpdateChangeNotes(r.Key, r.Author, first, "", nil); err != nil {
		t.Fatal(err)
	}
	err := e.s.UpdateChangeNotes(r.Key, r.Author, ChangeNotes{Testing: strings.Repeat("b", 8000)}, "", nil)
	if err == nil || !strings.Contains(err.Error(), "exceed") {
		t.Fatalf("update past the limit = %v; want a refusal", err)
	}
	if got := e.record().Notes; got != first {
		t.Fatalf("a refused update changed the notes: %d bytes of testing", len(got.Testing))
	}
}

func TestReopenedLocalChangeRefusesNotesPastTheLimit(t *testing.T) {
	e := newEnv(t)
	r, dir := localChange(t, e)
	actor := authority.Actor{Role: authority.Owner}
	if _, _, err := e.s.OpenChange(context.Background(), actor, e.author, OpenChangeOptions{Dir: dir, Notes: ChangeNotes{Risks: strings.Repeat("a", 8000)}}); err != nil {
		t.Fatal(err)
	}
	_, _, err := e.s.OpenChange(context.Background(), actor, e.author, OpenChangeOptions{Dir: dir, Notes: ChangeNotes{Questions: strings.Repeat("b", 8000)}})
	if err == nil || !strings.Contains(err.Error(), "exceed") {
		t.Fatalf("reopen past the limit = %v; want a refusal", err)
	}
	got, _ := e.s.Store.Get(r.Key)
	if got.Notes.Questions != "" || got.Notes.Risks == "" {
		t.Fatalf("a refused reopen changed the notes: %+v", got.Notes)
	}
}

func TestReviewDeltaExcludesRebasedMainChanges(t *testing.T) {
	e := newEnv(t)
	r, dir := localChange(t, e)
	oldHead := r.Head
	changeGit(t, dir, "checkout", "main")
	if err := os.WriteFile(filepath.Join(dir, "main-only.txt"), []byte("unrelated main change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changeGit(t, dir, "add", ".")
	changeGit(t, dir, "commit", "-m", "unrelated main change")
	changeGit(t, dir, "checkout", "feature")
	changeGit(t, dir, "rebase", "main")
	r.Base = "main"
	for _, update := range []bool{false, true} {
		if update {
			changeCommit(t, dir, "feature fixed\n")
		}
		r.Head = changeGit(t, dir, "rev-parse", "HEAD")
		if _, err := e.s.prepareReviewTask(context.Background(), r, oldHead); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(state.ReviewInputsDir(e.s.Store.Root, r.Key), digest(r.Head)[:16]+"-0.json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var input ReviewContext
		if err := json.Unmarshal(data, &input); err != nil {
			t.Fatal(err)
		}
		delta, err := os.ReadFile(input.DeltaPath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(delta), "main-only.txt") || strings.Contains(string(delta), "unrelated main change") {
			t.Fatalf("delta includes main changes:\n%s", delta)
		}
		if update && !strings.Contains(string(delta), r.Head[:7]) {
			t.Fatalf("delta omits the author's fix:\n%s", delta)
		}
	}
}

// A reply records the head the change has now, not the last one a poll saw. An
// author who pushes a fix and replies at once names the new commit in the text;
// a reply stored on the old head let the reviewer judge the wrong revision
// (D-177).
func TestReplyRecordsTheHeadPushedSinceTheLastPoll(t *testing.T) {
	e := newEnv(t)
	r, dir := localChange(t, e)
	e.tick()
	r, _ = e.s.Store.Get(r.Key)
	findings := []Finding{{Severity: "blocking", File: "code.txt", Line: 1, Why: "Missing bounds check", Fix: "Check the index"}}
	if err := e.s.VerdictWithFindings(context.Background(), r.Key, r.Reviewer, r.Head, "changes", "Fix bounds", 0, findings); err != nil {
		t.Fatal(err)
	}
	reviewed, _ := e.s.Store.Get(r.Key)
	pushed := changeCommit(t, dir, "bounds checked\n")
	// No poll runs between the push and the reply.
	if err := e.s.ReplyFinding(context.Background(), r.Key, r.Author, e.author.ID, reviewed.Findings[0].ID, "fixed", "Fixed in "+pushed[:9]); err != nil {
		t.Fatal(err)
	}
	got, _ := e.s.Store.Get(r.Key)
	if head := got.Findings[0].Replies[0].Head; head != pushed {
		t.Fatalf("reply head = %q, want the pushed head %q (stale was %q)", head, pushed, r.Head)
	}
}

// A finding from an earlier round stays repliable after a new head clears the
// current findings (D-271). The reply is recorded in its round and does not
// reopen the review the new head passed.
func TestReplyToAFindingFromAnEarlierRoundIsRecorded(t *testing.T) {
	e := newEnv(t)
	r, _ := localChange(t, e)
	e.tick()
	r, _ = e.s.Store.Get(r.Key)
	findings := []Finding{{Severity: "blocking", File: "code.txt", Line: 1, Why: "Missing bounds check", Fix: "Check the index"}}
	if err := e.s.VerdictWithFindings(context.Background(), r.Key, r.Reviewer, r.Head, "changes", "Fix bounds", 0, findings); err != nil {
		t.Fatal(err)
	}
	reviewed, _ := e.s.Store.Get(r.Key)
	id := reviewed.Findings[0].ID
	// A later round approved a new head, so the current findings are empty.
	if err := e.s.Store.update(func(st *State) error {
		rec := recordByID(st, r.Key)
		rec.Findings = []Finding{}
		rec.Verdict = "approve"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.ReplyFinding(context.Background(), r.Key, r.Author, e.author.ID, id, "fixed", "Fixed in the new head"); err != nil {
		t.Fatalf("reply to an earlier round's finding = %v, want it recorded", err)
	}
	got, _ := e.s.Store.Get(r.Key)
	replies := got.ReviewRounds[0].Findings[0].Replies
	if len(replies) != 1 || replies[0].Message != "Fixed in the new head" {
		t.Fatalf("round replies = %+v, want the reply", replies)
	}
	if got.Verdict != "approve" {
		t.Fatalf("verdict = %q, want the approval kept", got.Verdict)
	}
	if err := e.s.ReplyFinding(context.Background(), r.Key, r.Author, e.author.ID, "f99-9", "fixed", "x"); err == nil {
		t.Fatal("a reply to a finding no round has was accepted")
	}
}
