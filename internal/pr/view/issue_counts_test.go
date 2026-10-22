package view

import (
	"encoding/json"
	"strings"
	"testing"
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
