package watch

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestCommentOwnerPrefersStoredIDsThenLegacyMarkers(t *testing.T) {
	owned := map[string]string{
		"issue-comments:10": "ses_stored_issue",
		"reviews:20":        "ses_stored_review",
	}
	for _, tc := range []struct {
		name string
		c    Comment
		want string
	}{
		{"stored comment ID", Comment{ID: 10, Kind: "issue-comments"}, "ses_stored_issue"},
		{"inline comment of a claimed review", Comment{ID: 11, Kind: "comments", ReviewID: 20}, "ses_stored_review"},
		{"legacy body marker", Comment{ID: 12, Kind: "issue-comments", Body: Marker("ses_legacy") + " text"}, "ses_legacy"},
		{"stored ID beats a legacy marker", Comment{ID: 10, Kind: "issue-comments", Body: Marker("ses_legacy")}, "ses_stored_issue"},
		{"unattributed owner comment", Comment{ID: 13, Kind: "issue-comments", Body: "plain"}, ""},
	} {
		if got := commentOwner(owned, tc.c); got != tc.want {
			t.Errorf("%s: commentOwner() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestStoredCommentSessionRoutesAuthorAndReviewerComments(t *testing.T) {
	pr := PRContext{URL: "https://github.com/owner/repo/pull/17", OwnerLogin: "owner", AuthorSession: "ses_author"}
	events := []Event{
		{ID: "issue-comments:1", Kind: "issue-comments", Commenter: "owner", Text: "my own progress", Session: "ses_author"},
		{ID: "issue-comments:2", Kind: "issue-comments", Commenter: "owner", Text: "owner feedback"},
		{ID: "issue-comments:3", Kind: "issue-comments", Commenter: "owner", Text: "reviewer finding", Session: "ses_reviewer"},
		{ID: "issue-comments:4", Kind: "issue-comments", Commenter: "owner", Text: "older reply " + Marker("ses_author")},
	}
	decisions := (Rules{}).Triage(pr, events)
	want := []Decision{
		{Action: "drop", Reason: "author_marker"},
		{Action: "keep", Reason: "owner"},
		{Action: "keep", Reason: "possibly_useful_feedback"},
		{Action: "drop", Reason: "author_marker"},
	}
	for i := range want {
		if decisions[i] != want[i] {
			t.Errorf("event %d: %+v, want %+v", i, decisions[i], want[i])
		}
	}
}

// A comment Dyna posted for the author routes by its stored comment ID and is
// not echoed back to the author as owner feedback.
func TestTickDropsAuthorCommentsByIDWithoutMarkers(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	if err := e.s.Comment(context.Background(), refRecord(r), e.author.ID, "my own progress note", true); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(os.Getenv("PR_POSTED"))
	if strings.Contains(string(body), "dyna:") {
		t.Fatalf("posted comment carries a marker: %s", body)
	}
	e.addComment(123, "issue-comments", "my own progress note", "owner")
	e.addComment(124, "issue-comments", "real owner feedback", "owner")
	e.tick()
	if len(e.sent) != 1 || strings.Contains(e.sent[0].Text, "my own progress note") || !strings.Contains(e.sent[0].Text, "real owner feedback") {
		t.Fatalf("author comment routing by stored ID: %+v", e.sent)
	}
}

// The comment ID claim happens in the same state update as the post, so a
// watcher tick cannot observe the comment before its ID is recorded.
func TestCommentClaimsIDWithThePost(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	if err := e.s.Comment(context.Background(), refRecord(r), e.author.ID, "progress", true); err != nil {
		t.Fatal(err)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if st.CommentSessions["issue-comments:123"] != e.author.ID {
		t.Fatalf("comment ID was not claimed with the post: %+v", st.CommentSessions)
	}
}

// When GitHub returns no comment ID the call fails loudly; a silent loss would
// leave the comment unattributed.
func TestCommentFailsWhenGitHubReturnsNoID(t *testing.T) {
	e := newEnv(t)
	e.track()
	e.f.Raw = map[string]string{"repos/owner/repo/issues/17/comments": "not json"}
	e.write()
	err := e.s.Comment(context.Background(), Ref{"github.com", "owner/repo", 17}, e.author.ID, "progress", true)
	if err == nil || !strings.Contains(err.Error(), "do not post it again") {
		t.Fatalf("unattributed comment returned %v", err)
	}
}

func TestPostReviewClaimsReviewIDAndItsInlineComments(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	id, err := e.s.PostReview(context.Background(), refRecord(r), e.author.ID, "Review at parser.go:42.", r.Head,
		[]ReviewComment{{Path: "parser.go", Line: 42, Body: "bound check"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if id != 123 {
		t.Fatalf("review ID = %d", id)
	}
	body, _ := os.ReadFile(os.Getenv("PR_POSTED"))
	for _, want := range []string{`"commit_id":"` + r.Head + `"`, `"event":"COMMENT"`, `"path":"parser.go"`, `"line":42`, "bound check"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("review request misses %s: %s", want, body)
		}
	}
	if strings.Contains(string(body), "dyna:") {
		t.Fatalf("review request carries a marker: %s", body)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if st.CommentSessions["reviews:123"] != e.author.ID {
		t.Fatalf("review ID was not claimed: %+v", st.CommentSessions)
	}
	// Inline comments of a claimed review belong to its reviewer's session.
	inline := Comment{ID: 900, Kind: "comments", ReviewID: 123, Body: "bound check"}
	if got := commentOwner(st.CommentSessions, inline); got != e.author.ID {
		t.Fatalf("inline comment of the claimed review routes to %q", got)
	}
}

func TestPublicationMarkersCannotOverrideCaller(t *testing.T) {
	for _, review := range []bool{false, true} {
		for _, caller := range []string{"", "ses_authenticated"} {
			t.Run(fmt.Sprintf("review=%v/caller=%s", review, caller), func(t *testing.T) {
				e := newEnv(t)
				r := e.track()
				body := Marker("ses_impersonated") + " feedback"
				kind := "issue-comments"
				var err error
				if review {
					kind = "reviews"
					_, err = e.s.PostReview(t.Context(), refRecord(r), caller, body, r.Head, nil, true)
				} else {
					err = e.s.Comment(t.Context(), refRecord(r), caller, body, true)
				}
				if err != nil {
					t.Fatal(err)
				}
				st, err := e.s.Store.Read()
				if err != nil {
					t.Fatal(err)
				}
				if got := st.CommentSessions[kind+":123"]; got != caller {
					t.Fatalf("post belongs to %q, want caller %q", got, caller)
				}
				posted, err := os.ReadFile(os.Getenv("PR_POSTED"))
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(posted), "dyna:") {
					t.Fatalf("posted marker: %s", posted)
				}
			})
		}
	}
}
