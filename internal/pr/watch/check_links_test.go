package watch

import "testing"

func TestCheckCommentsFallbackByNameOrApp(t *testing.T) {
	s := Snapshot{
		Statuses: []Status{{Context: "CodeRabbit", State: "success"}},
		Checks:   []Check{{Name: "Review", Status: "completed", Conclusion: "success"}},
		Comments: []Comment{
			{ID: 1, User: User{Login: "coderabbitai[bot]", Type: "Bot"}, URL: "old", Updated: "2026-10-17T01:00:00Z"},
			{ID: 2, User: User{Login: "coderabbitai[bot]", Type: "Bot"}, URL: "latest-review", Submitted: "2026-10-17T02:00:00Z"},
			{ID: 3, User: User{Login: "coderabbitai[bot]", Type: "Bot"}, URL: "pending", State: "PENDING", Updated: "2026-10-17T03:00:00Z"},
			{ID: 4, User: User{Login: "CodeRabbit", Type: "User"}, URL: "human", Updated: "2026-10-17T04:00:00Z"},
			{ID: 5, User: User{Login: "copilot-pull-request-reviewer[bot]"}, URL: "copilot", Created: "2026-10-17T05:00:00Z"},
		},
	}
	s.Checks[0].App.Slug = "copilot"
	summaries := s.CheckSummaries(Ref{})
	if len(summaries) != 2 || summaries[0].CommentURL != "latest-review" || summaries[1].CommentURL != "copilot" {
		t.Fatalf("summaries: %+v", summaries)
	}
	// A known details URL wins. Unknown or similar names cannot borrow a link.
	s.Statuses[0].URL = "details"
	s.Checks[0].App.Slug = "coderabbit-unrelated"
	summaries = s.CheckSummaries(Ref{})
	if summaries[0].URL != "details" || summaries[0].CommentURL != "" || summaries[1].CommentURL != "" {
		t.Fatalf("summaries: %+v", summaries)
	}
}

func TestCheckCommentsFallbackAppNameAndExactBotIdentity(t *testing.T) {
	s := Snapshot{Checks: []Check{{Name: "check"}}, Comments: []Comment{{ID: 1, User: User{Login: "greptile-apps[bot]"}, URL: "greptile"}}}
	s.Checks[0].App.Name = "Greptile"
	if got := s.CheckSummaries(Ref{})[0].CommentURL; got != "greptile" {
		t.Fatalf("comment URL = %q", got)
	}
	s.Comments[0].User.Login = "unrelated-greptile[bot]"
	if got := s.CheckSummaries(Ref{})[0].CommentURL; got != "" {
		t.Fatalf("unrelated bot matched: %q", got)
	}
	s.Checks[0].App.Name = "custom-app"
	s.Comments[0].User = User{Login: "custom-app[bot]"}
	if got := s.CheckSummaries(Ref{})[0].CommentURL; got != "greptile" {
		t.Fatalf("custom app did not match: %q", got)
	}
}
