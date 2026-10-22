package watch

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLocalReviewDefaultPreservesMarkdownWithoutGitHubWrites(t *testing.T) {
	for _, legacy := range []string{"", "local", "retired-value"} {
		t.Run("setting="+legacy, func(t *testing.T) {
			e := newEnv(t)
			e.setCI("success", 1)
			r := e.track()
			if err := e.s.Store.update(func(st *State) error {
				st.Reviewers[r.RoomID] = Reviewer{Profile: "judge", ReviewPublishing: legacy}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			e.tick()
			r = e.record()
			task := e.launches[0]
			if r.ReviewPublishing != PublishingLocal || !strings.Contains(task, "Read GitHub without posting") || !strings.Contains(task, "--head "+r.Head) || strings.Contains(task, "gh pr review --comment") {
				t.Fatalf("local assignment: %+v, %s", r, task)
			}
			feedback := "## Findings\n\n- `internal/parser.go:42`: check the bound.\n\n" + strings.Repeat("Keep this detail.\n", 200) + "\n`tail.go:99`: final finding."
			for _, bad := range []struct{ actor, head string }{{r.Reviewer, ""}, {e.author.ID, r.Head}, {r.Reviewer, "old-head"}} {
				if err := e.s.Verdict(context.Background(), r.Key, bad.actor, bad.head, "changes", feedback, 0); err == nil {
					t.Fatal("accepted an unbound or unauthorized local review")
				}
			}
			if err := e.s.Verdict(context.Background(), r.Key, r.Reviewer, r.Head, "changes", feedback, 0); err != nil {
				t.Fatal(err)
			}
			got := e.record()
			if got.Summary != feedback || got.ReviewID != 0 || len(e.sent) != 1 || e.sent[0].Target != e.author.ID || !strings.Contains(e.sent[0].Text, feedback) {
				t.Fatalf("local feedback lost content or routing: %+v / %+v", got, e.sent)
			}
			if err := e.s.Verdict(context.Background(), r.Key, r.Reviewer, r.Head, "changes", feedback, 0); err != nil || len(e.sent) != 1 {
				t.Fatalf("local verdict replay: %v, %+v", err, e.sent)
			}
			calls, err := os.ReadFile(e.calls)
			if err != nil {
				t.Fatal(err)
			}
			writes := false
			for _, call := range strings.Split(string(calls), "\n") {
				if strings.Contains(call, "--method POST") && !strings.Contains(call, " graphql ") {
					writes = true
				}
			}
			if writes || strings.Contains(string(calls), "pr review") {
				t.Fatalf("local review wrote GitHub: %s", calls)
			}
		})
	}
}

func TestGitHubAssignmentClaimsPublishedReviewAndKeepsItsMode(t *testing.T) {
	e := newEnv(t)
	e.setCI("success", 1)
	r := e.track()
	if err := e.s.ConfigurePublishing(r.RoomID, PublishingGitHub); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Configure(r.RoomID, Reviewer{Profile: "judge"}); err != nil {
		t.Fatal(err)
	}
	e.tick()
	r = e.record()
	if r.ReviewPublishing != PublishingGitHub || !strings.Contains(e.launches[0], "dyna git pr review") || !strings.Contains(e.launches[0], "--comments-file") || !strings.Contains(e.launches[0], "--review-id") {
		t.Fatalf("GitHub assignment: %+v / %+v", r, e.launches)
	}
	if strings.Contains(e.launches[0], "dyna:") {
		t.Fatalf("assignment still asks for a comment marker: %s", e.launches[0])
	}
	if err := e.s.ConfigurePublishing(r.RoomID, PublishingLocal); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Verdict(context.Background(), r.Key, r.Reviewer, r.Head, "approve", "review", 0); err == nil {
		t.Fatal("accepted GitHub assignment without published review")
	}
	e.addComment(88, "reviews", "Review at parser.go:42.", "owner")
	for _, invalid := range []struct{ body, commit, state string }{
		{Marker(r.Reviewer) + " wrong head", "old-head", "COMMENTED"},
		{"Unmarked draft", r.Head, "PENDING"},
	} {
		e.f.Comments[0].Body, e.f.Comments[0].Commit, e.f.Comments[0].State = invalid.body, invalid.commit, invalid.state
		e.write()
		if err := e.s.Verdict(context.Background(), r.Key, r.Reviewer, r.Head, "approve", "review", 88); err == nil {
			t.Fatalf("accepted invalid published review: %+v", invalid)
		}
	}
	e.f.Comments[0].Body, e.f.Comments[0].Commit, e.f.Comments[0].State = "Review at parser.go:42.", r.Head, "COMMENTED"
	e.write()
	if err := e.s.Verdict(context.Background(), r.Key, r.Reviewer, r.Head, "approve", "review", 88); err != nil {
		t.Fatal(err)
	}
	if st, err := e.s.Store.Read(); err != nil || st.CommentSessions["reviews:88"] != r.Reviewer {
		t.Fatalf("published review ID was not claimed by its reviewer: %+v, %v", st.CommentSessions, err)
	}
	if e.record().ReviewPublishing != PublishingGitHub {
		t.Fatal("room setting rewrote an active assignment")
	}
	e.f.Pull.Head.SHA = strings.Repeat("b", 40)
	e.setCI("success", 2)
	e.tick()
	r = e.record()
	if r.ReviewPublishing != PublishingLocal || !strings.Contains(e.launches[len(e.launches)-1], "Dyna-only") {
		t.Fatalf("new assignment ignored current room setting: %+v", r)
	}
	if err := e.s.ConfigurePublishing(r.RoomID, PublishingGitHub); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Verdict(context.Background(), r.Key, r.Reviewer, r.Head, "changes", "Local review at file.go:42", 0); err != nil {
		t.Fatalf("local assignment changed mode mid-round: %v", err)
	}
	if e.record().ReviewPublishing != PublishingLocal {
		t.Fatal("local assignment lost its bound mode")
	}
}

// A reviewer cannot claim a review the owner submitted before the round was
// assigned; only reviews from the round window route to the reviewer.
func TestVerdictRefusesReviewsSubmittedBeforeTheRound(t *testing.T) {
	e := newEnv(t)
	e.setCI("success", 1)
	// The test starts from the assigned round. Watcher polling and reviewer
	// selection have separate coverage and are unrelated to review timestamps.
	ref := Ref{"github.com", "owner/repo", 17}
	r := Record{
		Key: ref.Key(), ChangeID: ref.Key(), Kind: "github", Host: ref.Host, Repo: ref.Repo, Number: ref.Number, URL: ref.URL(),
		RoomID: e.author.RoomID, Author: e.author.ID, Head: e.f.Pull.Head.SHA, RoundHead: e.f.Pull.Head.SHA,
		State: "open", Watching: true, CI: "passed", ReviewPublishing: PublishingGitHub,
		Reviewer: "ses_20261017-000003-cccccccc", ReviewerMode: "session", Round: 1, RoundStarted: e.clock,
	}
	if err := e.s.Store.update(func(st *State) error {
		st.Records[r.Key] = &r
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.addComment(88, "reviews", "owner review from before the round", "owner")
	e.f.Comments[0].Body, e.f.Comments[0].Commit, e.f.Comments[0].State = "owner review from before the round", r.Head, "COMMENTED"
	e.f.Comments[0].Submitted = e.clock.Add(-time.Hour).Format(time.RFC3339)
	e.write()
	if err := e.s.Verdict(context.Background(), r.Key, r.Reviewer, r.Head, "approve", "review", 88); err == nil {
		t.Fatal("claimed a review submitted before the round started")
	}
	e.f.Comments[0].Submitted = e.clock.Add(time.Minute).Format(time.RFC3339)
	e.write()
	if err := e.s.Verdict(context.Background(), r.Key, r.Reviewer, r.Head, "approve", "review", 88); err != nil {
		t.Fatalf("refused a review submitted after the round started: %v", err)
	}
}

func TestReviewPublishingSettingsPreserveRolesAndDefaultLocal(t *testing.T) {
	e := newEnv(t)
	const roomID = "room-config"
	if mode, err := e.s.Publishing(roomID); err != nil || mode != PublishingLocal {
		t.Fatalf("default = %s, %v", mode, err)
	}
	if err := e.s.Configure(roomID, Reviewer{Profile: "judge"}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.ConfigurePublishing(roomID, PublishingGitHub); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Configure(roomID, Reviewer{Profile: "reviewer"}); err != nil {
		t.Fatal(err)
	}
	st, _ := e.s.Store.Read()
	if got := st.Reviewers[roomID]; got.Profile != "reviewer" || got.ReviewPublishing != PublishingGitHub {
		t.Fatalf("settings overwritten: %+v", got)
	}
	if err := e.s.ConfigurePublishing(roomID, "invalid"); err == nil {
		t.Fatal("invalid setting accepted")
	}
	if err := e.s.ConfigureRoom(roomID, Reviewer{ReviewPublishing: "invalid"}); err == nil {
		t.Fatal("invalid full setting accepted")
	}
	if mode, _ := e.s.Publishing(roomID); mode != PublishingGitHub {
		t.Fatal("invalid write changed settings")
	}
}

func TestCommentRequiresRoomOrExplicitPublishingOptIn(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	ref := refRecord(r)
	if err := e.s.Comment(context.Background(), ref, e.author.ID, "local by default", false); err == nil || !strings.Contains(err.Error(), "--publish") {
		t.Fatalf("default comment error = %v", err)
	}
	if _, err := os.Stat(os.Getenv("PR_POSTED")); !os.IsNotExist(err) {
		t.Fatalf("default comment wrote GitHub: %v", err)
	}
	if err := e.s.Comment(context.Background(), ref, e.author.ID, "explicit comment", true); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(os.Getenv("PR_POSTED"))
	if !strings.Contains(string(body), "explicit comment") || strings.Contains(string(body), "dyna:") {
		t.Fatalf("explicit publish must not carry a marker: %s", body)
	}
	if st, err := e.s.Store.Read(); err != nil || st.CommentSessions["issue-comments:123"] != e.author.ID {
		t.Fatalf("comment ID not recorded for routing: %+v, %v", st.CommentSessions, err)
	}
	if err := e.s.ConfigurePublishing(r.RoomID, PublishingGitHub); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Comment(context.Background(), ref, e.author.ID, "room opt-in", false); err != nil {
		t.Fatal(err)
	}
	body, _ = os.ReadFile(os.Getenv("PR_POSTED"))
	if !strings.Contains(string(body), "room opt-in") {
		t.Fatalf("room opt-in did not publish: %s", body)
	}
}

func TestReviewLimitRejectsBeforeSavingAndKeepsAcceptedMarkdown(t *testing.T) {
	e := newEnv(t)
	e.setCI("success", 1)
	r := e.track()
	e.s.Configure(r.RoomID, Reviewer{Profile: "judge"})
	e.tick()
	r = e.record()
	if err := e.s.Verdict(context.Background(), r.Key, r.Reviewer, r.Head, "changes", strings.Repeat("x", MaxReviewBytes+1), 0); err == nil || !strings.Contains(err.Error(), "nothing was saved") {
		t.Fatalf("over-limit review = %v", err)
	}
	if e.record().Verdict != "" || len(e.sent) != 0 {
		t.Fatal("over-limit review saved state")
	}
	full := strings.Repeat("x", MaxReviewBytes-len("\nfile.go:99")) + "\nfile.go:99"
	if err := e.s.Verdict(context.Background(), r.Key, r.Reviewer, r.Head, "changes", full, 0); err != nil {
		t.Fatal(err)
	}
	if e.record().Summary != full || !strings.Contains(e.sent[0].Text, full) {
		t.Fatal("accepted feedback was clipped")
	}
}

func TestConfigureRoomRejectsInvalidSettingsWithoutChanges(t *testing.T) {
	e := newEnv(t)
	const roomID = "room-config"
	if err := e.s.ConfigureRoom(roomID, Reviewer{Profile: "judge", ReviewPublishing: PublishingLocal}); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []Reviewer{
		{Profile: "missing-profile", ReviewPublishing: PublishingGitHub},
		{Profile: "reviewer", ReviewPublishing: "invalid"},
	} {
		if err := e.s.ConfigureRoom(roomID, cfg); err == nil {
			t.Fatal("invalid settings accepted")
		}
		st, err := e.s.Store.Read()
		if err != nil {
			t.Fatal(err)
		}
		if got := st.Reviewers[roomID]; got.Profile != "judge" || got.ReviewPublishing != PublishingLocal {
			t.Fatalf("invalid request changed settings: %+v", got)
		}
	}
}
