package view

import (
	"encoding/json"
	"strings"
	"testing"

	"dyna-agent/internal/pr/state"
)

func TestIssueRowsCarryCommentCountAndAssignees(t *testing.T) {
	for _, q := range []string{issueListQuery, issueViewQuery} {
		if !strings.Contains(q, "commentCount: comments { totalCount }") || !strings.Contains(q, "assignees(first:") {
			t.Fatalf("query lacks the comment count or assignees:\n%s", q)
		}
	}
	raw := `{"number":3,"title":"T","state":"OPEN","author":{"login":"sam"},
	 "commentCount":{"totalCount":4},"assignees":{"nodes":[{"login":"sol"}]},
	 "labels":{"nodes":[]},"closedByPullRequestsReferences":{"nodes":[]}}`
	flat, err := flattenIssue(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	var item ghItem
	if err := json.Unmarshal(flat, &item); err != nil {
		t.Fatal(err)
	}
	got := item.item()
	if got.CommentCount != 4 || len(got.Assignees) != 1 || got.Assignees[0].Login != "sol" {
		t.Fatalf("item = %+v", got)
	}
}

// Worked issues are read by number, one query per host, so an issue with live
// room work reaches the Tracked view whatever page the GitHub list shows. A
// deleted issue answers null and is left out.
func TestWorkedIssuesAreReadByNumber(t *testing.T) {
	queries := workedIssueQueries([]state.IssueWork{
		{Repo: "github.com/up/r", Number: 12}, {Repo: "github.com/o/r", Number: 3}, {Repo: "github.com/o/r", Number: 3},
		{Repo: `github.com/bad"/r`, Number: 9},
	})
	q := queries["github.com"]
	if len(queries) != 1 || !strings.Contains(q, `r0: repository(owner:"o",name:"r") { i3: issue(number:3)`) ||
		!strings.Contains(q, `r1: repository(owner:"up",name:"r") { i12: issue(number:12)`) || strings.Count(q, "issue(number:3)") != 1 || strings.Contains(q, "bad") {
		t.Fatalf("queries = %v", queries)
	}
	items, err := parseWorkedIssues([]byte(`{"data":{"r0":{"i3":{"number":3,"title":"Worked","state":"OPEN","url":"https://github.com/o/r/issues/3","author":{"login":"sam"},"labels":{"nodes":[]}}},"r1":{"i12":null}}}`))
	if err != nil || len(items) != 1 || items[0].Title != "Worked" || items[0].Labels == nil {
		t.Fatalf("items = %+v err=%v", items, err)
	}
}
