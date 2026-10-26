package watch

import (
	"encoding/json"
	"reflect"
	"testing"

	"dyna-agent/internal/pr/state"
)

func botSummaryCheck(id int64, name, app, status, conclusion string) Check {
	c := Check{ID: id, Name: name, Status: status, Conclusion: conclusion}
	c.App.Slug = app
	return c
}

func TestBotReviewsClassifiesCIAndLatestAttempts(t *testing.T) {
	s := Snapshot{Checks: []Check{
		botSummaryCheck(1, "build", "github-actions", "completed", "failure"),
		botSummaryCheck(2, "required", "third-party", "completed", "failure"),
		botSummaryCheck(3, "Required", "third-party", "completed", "success"),
		botSummaryCheck(4, "review", "greptile", "completed", "failure"),
		botSummaryCheck(5, "review", "greptile", "completed", "success"),
		botSummaryCheck(6, "security", "greptile", "in_progress", ""),
	}, Statuses: []Status{
		{ID: 1, Context: "required", State: "failure", Creator: User{Login: "required-bot"}},
		{ID: 2, Context: "analysis", State: "failure", Creator: User{Login: "CodeRabbitAI[bot]"}},
		{ID: 3, Context: "analysis", State: "success", Creator: User{Login: "CodeRabbitAI[bot]"}},
		{ID: 4, Context: "optional-actions-status", State: "failure", Creator: User{Login: "github-actions[bot]"}},
	}}
	want := []state.BotReview{
		{Bot: "githubactions", State: "changes_requested"},
		{Bot: "greptile", State: "pending"},
	}
	if got := s.BotReviews("required"); !reflect.DeepEqual(got, want) {
		t.Fatalf("BotReviews() = %#v, want %#v", got, want)
	}
	// A different input order must produce the same latest attempts and order.
	for i, j := 0, len(s.Checks)-1; i < j; i, j = i+1, j-1 {
		s.Checks[i], s.Checks[j] = s.Checks[j], s.Checks[i]
	}
	if got := s.BotReviews("required"); !reflect.DeepEqual(got, want) {
		t.Fatalf("reordered BotReviews() = %#v, want %#v", got, want)
	}
}

func TestBotReviewsCurrentHeadFindingsAndLatestReview(t *testing.T) {
	s := Snapshot{}
	s.Pull.Head.SHA = "current"
	bot := User{Login: "copilot-pull-request-reviewer[bot]", Type: "Bot"}
	s.Comments = []Comment{
		{ID: 9, Kind: "reviews", User: bot, Commit: "old", State: "CHANGES_REQUESTED", URL: "stale", Submitted: "2026-10-17T15:00:00Z"},
		{ID: 8, Kind: "comments", User: bot, Commit: "old", URL: "stale-inline"},
		{ID: 1, Kind: "reviews", User: bot, Commit: "current", State: "CHANGES_REQUESTED", URL: "review1", Submitted: "2026-10-17T12:00:00Z"},
		{ID: 2, Kind: "reviews", User: bot, Commit: "current", State: "APPROVED", URL: "review2", Submitted: "2026-10-17T13:00:00Z"},
		{ID: 3, Kind: "comments", User: bot, Commit: "current", URL: "inline", Created: "2026-10-17T14:00:00Z"},
		{ID: 4, Kind: "comments", User: User{Login: "human"}, Commit: "current"},
		{ID: 5, Kind: "comments", User: bot, Commit: ""},
	}
	want := []state.BotReview{{Bot: "copilot", State: "approved", Findings: 1, URL: "inline"}}
	if got := s.BotReviews(); !reflect.DeepEqual(got, want) {
		t.Fatalf("BotReviews() = %#v, want %#v", got, want)
	}
}

func TestBotReviewsIssueCommentsAndUnknownApps(t *testing.T) {
	s := Snapshot{}
	s.Pull.Head.SHA = "current"
	named := botSummaryCheck(1, "review", "", "completed", "failure")
	named.App.Name = "New Review App"
	unnamed := botSummaryCheck(2, "review", "", "completed", "success")
	unnamed.App.ID = 42
	s.Checks = []Check{named, unnamed, botSummaryCheck(3, "???", "", "queued", "")}
	s.Comments = []Comment{
		{ID: 1, Kind: "issue-comments", User: User{Login: "new-review-app[bot]"}, URL: "issue1", Updated: "2026-10-17T12:00:00Z"},
		{ID: 2, Kind: "issue-comments", User: User{Login: "new-review-app[bot]"}, URL: "issue2", Updated: "2026-10-17T12:00:00Z"},
		{ID: 3, Kind: "issue-comments", User: User{Login: "new-review-app[bot]"}, Commit: "old", URL: "stale", Updated: "2026-10-17T13:00:00Z"},
		{ID: 4, Kind: "issue-comments", User: User{Login: "another[bot]"}, URL: "comment-only"},
		{ID: 5, Kind: "reviews", User: User{Login: "stale[bot]"}, Commit: "old", State: "APPROVED"},
	}
	want := []state.BotReview{
		{Bot: "another", State: "commented", URL: "comment-only"},
		{Bot: "newreviewapp", State: "changes_requested", URL: "issue2"},
		{Bot: "unknown", State: "pending"},
	}
	if got := s.BotReviews(); !reflect.DeepEqual(got, want) {
		t.Fatalf("BotReviews() = %#v, want %#v", got, want)
	}
}

func TestBotReviewsReviewStatesAndPendingChecks(t *testing.T) {
	for _, tc := range []struct {
		name, review, checkStatus, conclusion, want string
	}{
		{"pending review", "PENDING", "", "", "pending"},
		{"commented review", "COMMENTED", "completed", "success", "commented"},
		{"requested changes", "CHANGES_REQUESTED", "completed", "success", "changes_requested"},
		{"running after approval", "APPROVED", "in_progress", "", "pending"},
		{"failed check after approval", "APPROVED", "completed", "failure", "changes_requested"},
		{"failed check after comment", "COMMENTED", "completed", "failure", "changes_requested"},
		{"latest check failed", "", "completed", "failure", "changes_requested"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := Snapshot{}
			s.Pull.Head.SHA = "current"
			if tc.review != "" {
				s.Comments = []Comment{{ID: 1, Kind: "reviews", Commit: "current", State: tc.review, User: User{Login: "greptile[bot]"}, URL: "review"}}
			}
			if tc.checkStatus != "" {
				s.Checks = []Check{botSummaryCheck(1, "review", "greptile", tc.checkStatus, tc.conclusion)}
			}
			got := s.BotReviews()
			if len(got) != 1 || got[0].State != tc.want || (tc.review == "PENDING" && got[0].URL != "") {
				t.Fatalf("BotReviews() = %#v, want state %q", got, tc.want)
			}
		})
	}
}

func TestBotReviewsMultipleChecksKeepFailuresAndReplaceAttempts(t *testing.T) {
	s := Snapshot{Checks: []Check{
		botSummaryCheck(3, "review", "greptile", "completed", "success"),
		botSummaryCheck(1, "review", "greptile", "completed", "failure"),
		botSummaryCheck(2, "security", "greptile", "completed", "failure"),
	}}
	if got := s.BotReviews(); len(got) != 1 || got[0].State != "changes_requested" {
		t.Fatalf("separate failing check was lost: %#v", got)
	}
	s.Checks = append(s.Checks, botSummaryCheck(4, "extra", "greptile", "in_progress", ""))
	if got := s.BotReviews(); len(got) != 1 || got[0].State != "pending" {
		t.Fatalf("mixed pending and failed checks lost pending state: %#v", got)
	}
	s.Checks = append(s.Checks,
		botSummaryCheck(5, "security", "greptile", "completed", "success"),
		botSummaryCheck(6, "extra", "greptile", "completed", "success"))
	if got := s.BotReviews(); len(got) != 0 {
		t.Fatalf("successful checks without a review became feedback: %#v", got)
	}
}

func TestBotReviewsInlineCommentsDoNotApprove(t *testing.T) {
	s := Snapshot{Checks: []Check{botSummaryCheck(1, "review", "greptile", "completed", "success")}}
	s.Pull.Head.SHA = "current"
	s.Comments = []Comment{{ID: 1, Kind: "comments", Commit: "current", User: User{Login: "greptile[bot]"}, URL: "inline"}}
	if got := s.BotReviews(); len(got) != 1 || got[0].State != "commented" || got[0].Findings != 1 {
		t.Fatalf("inline feedback was lost: %#v", got)
	}
	s.Checks = nil
	s.Comments[0].Commit = "old"
	if got := s.BotReviews(); len(got) != 0 {
		t.Fatalf("stale-only bot was retained: %#v", got)
	}
}

func TestBotReviewsSuccessfulCheckIsNotAnApproval(t *testing.T) {
	s := Snapshot{Statuses: []Status{{
		ID: 1, Context: "go-test", State: "success", Creator: User{Login: "arismoko", Type: "Bot"},
	}}}
	if got := s.BotReviews(); len(got) != 0 {
		t.Fatalf("BotReviews() invented an approval from a check: %#v", got)
	}
}

func TestBotReviewsRecordJSON(t *testing.T) {
	record := state.Record{CIReason: "no workflows", BotReviews: []state.BotReview{{Bot: "greptile", State: "approved", Findings: 0, URL: "review"}}}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["ciReason"]) != `"no workflows"` || string(fields["botReviews"]) != `[{"bot":"greptile","state":"approved","findings":0,"url":"review"}]` {
		t.Fatalf("unexpected persisted fields: ciReason=%s botReviews=%s", fields["ciReason"], fields["botReviews"])
	}
}
