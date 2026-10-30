package ghclient

import (
	"context"
	"testing"

	"dyna-agent/internal/approvals"
	"dyna-agent/internal/room"
)

func TestWritePolicy(t *testing.T) {
	cases := []struct {
		name                string
		args                []string
		target              Target
		author              string
		own, head, readOnly bool
		rule                RuleID
		class               approvals.Class
		reason              Reason
	}{
		{name: "room create", args: []string{"pr", "create"}, target: TargetRoom, rule: "pr-create-room", class: approvals.ClassAuto},
		{name: "REST create requires agent", args: []string{"api", "repos/o/r/pulls", "-f", "title=x"}, target: TargetRoom, rule: "pr-create-api", class: approvals.ClassAgent},
		{name: "upstream create", args: []string{"pr", "create"}, target: TargetUpstream, rule: "pr-create-upstream", class: approvals.ClassAgent},
		{name: "own tracked pr", args: []string{"pr", "comment", "3"}, target: TargetRoom, own: true, rule: "pr-own", class: approvals.ClassAuto},
		{name: "other pr", args: []string{"pr", "comment", "3"}, target: TargetRoom, rule: "pr-collaborate", class: approvals.ClassAgent},
		{name: "review comment", args: []string{"pr", "review", "3", "--comment"}, target: TargetUpstream, rule: "review-comment", class: approvals.ClassAgent},
		{name: "review verdict", args: []string{"pr", "review", "3", "--approve"}, target: TargetRoom, rule: "review-verdict", class: approvals.ClassOwner},
		{name: "issue create", args: []string{"issue", "create"}, target: TargetRoom, rule: "issue-collaborate", class: approvals.ClassAgent},
		{name: "root closes own upstream issue", args: []string{"issue", "close", "3"}, target: TargetUpstream, author: "BOT", rule: "item-own", class: approvals.ClassAgent},
		{name: "foreign room close", args: []string{"issue", "close", "3"}, target: TargetRoom, author: "other", rule: "item-foreign-room", class: approvals.ClassOwner},
		{name: "foreign upstream close", args: []string{"issue", "close", "3"}, target: TargetUpstream, author: "other", rule: "item-foreign-upstream", reason: ReasonForeignItem},
		{name: "own comment", args: []string{"api", "repos/o/r/issues/comments/7", "-X", "PATCH", "-f", "body=x"}, target: TargetRoom, author: "bot", rule: "comment-own", class: approvals.ClassAgent},
		{name: "foreign comment", args: []string{"api", "repos/o/r/issues/comments/7", "-X", "DELETE"}, target: TargetRoom, author: "other", rule: "comment-foreign", reason: ReasonForeignComment},
		{name: "input foreign comment", args: []string{"api", "repos/o/r/issues/comments/7", "-X", "PATCH", "--input", "body.json"}, target: TargetRoom, author: "other", rule: "comment-foreign", reason: ReasonForeignComment},
		{name: "unknown comment author", args: []string{"api", "repos/o/r/issues/comments/7", "-X", "DELETE"}, target: TargetUpstream, rule: "comment-foreign", reason: ReasonForeignComment},
		{name: "failed rerun", args: []string{"run", "rerun", "3", "--failed"}, target: TargetRoom, rule: "rerun-failed", class: approvals.ClassAgent},
		{name: "own run", args: []string{"run", "cancel", "3"}, target: TargetRoom, author: "bot", rule: "run-own", class: approvals.ClassAgent},
		{name: "fork upstream", args: []string{"repo", "fork", "o/r"}, target: TargetUpstream, rule: "fork-upstream", class: approvals.ClassAgent},
		{name: "other repo", args: []string{"pr", "review", "3", "--approve"}, target: TargetOther, rule: "other-repository", reason: ReasonOther},
		{name: "read only", args: []string{"pr", "create"}, target: TargetRoom, readOnly: true, rule: "read-only", reason: ReasonReadOnly},
		{name: "delete branch before own", args: []string{"pr", "close", "3", "-d"}, target: TargetUpstream, author: "bot", rule: "upstream-branch-delete", reason: ReasonBranchDelete},
		{name: "foreign head", args: []string{"pr", "edit", "3"}, target: TargetUpstream, rule: "upstream-edit-head", reason: ReasonForeignHead},
		{name: "own head", args: []string{"pr", "edit", "3"}, target: TargetUpstream, head: true, rule: "pr-collaborate", class: approvals.ClassAgent},
		{name: "unknown upstream", args: []string{"pr", "merge", "3"}, target: TargetUpstream, rule: "upstream-verb", reason: ReasonUpstreamVerb},
		{name: "unknown room", args: []string{"pr", "merge", "3"}, target: TargetRoom, rule: "room-owner", class: approvals.ClassOwner},
		{name: "api close", args: []string{"api", "repos/o/r/issues/3", "-X", "PATCH", "-f", "state=closed"}, target: TargetUpstream, author: "bot", rule: "item-own", class: approvals.ClassAgent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := room.Record{GitHubAccess: room.GitHubReadWrite}
			if tc.readOnly {
				rec.GitHubAccess = room.GitHubReadOnly
			}
			w := ParseWrite(tc.args, "github.com", "o/r", tc.target)
			calls := 0
			got, err := Decide(context.Background(), rec, w, WriteLookups{Login: "bot", AuthorOf: func(context.Context, ItemRef) (string, error) { calls++; return tc.author, nil }, OwnPR: func(context.Context, ItemRef) bool { return tc.own }, ForkHead: func(context.Context, Write) bool { return tc.head }})
			if err != nil || got.Rule != tc.rule || got.Class != tc.class || got.Refusal != tc.reason || calls > 1 {
				t.Fatalf("decision = %+v, err %v, author calls %d", got, err, calls)
			}
		})
	}
}
