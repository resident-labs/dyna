package ghclient

import (
	"context"
	"errors"
	"strings"
	"testing"

	"dyna-agent/internal/approvals"
)

func TestClassify(t *testing.T) {
	const room = "room-bot"
	authors := map[string]string{
		"issue:7": room, "issue:8": "someone-else", "pr:12": room, "pr:13": "someone-else",
		"pr:": room, "run:99": room, "run:98": "someone-else",
	}
	endpointRepos := map[string]string{"other/repo": "someone-else"}
	in := ClassifyInput{
		Host: "github.com", Repo: "me/fork", Upstream: "github.com/org/project", Login: room,
		AuthorOf: func(_ context.Context, ref ItemRef) (string, error) {
			if author, ok := endpointRepos[ref.Repo]; ok {
				return author, nil
			}
			author, ok := authors[string(ref.Kind)+":"+ref.Target]
			if !ok {
				return "", errors.New("lookup failed")
			}
			return author, nil
		},
	}
	const read, agent, owner = approvals.ClassRead, approvals.ClassAgent, approvals.ClassOwner
	tests := []struct {
		name string
		cmd  string
		want approvals.Class
	}{
		{"pr view is a read", "pr view 3", read},
		{"api GET is a read", "api repos/o/r/issues", read},
		{"api graphql query is a read", "api graphql -f query=query{viewer{login}}", read},
		{"workflow list is a read", "workflow list", read},
		{"issue create", "issue create --title t --body b", agent},
		{"issue edit", "issue edit 5 --add-label bug", agent},
		{"issue comment", "issue comment 5 --body hi", agent},
		{"pr create", "pr create --title t --body b", agent},
		{"pr edit", "pr edit 5 --title t", agent},
		{"pr ready", "pr ready 5", agent},
		{"pr comment", "pr comment 5 -b hi", agent},
		{"pr review --comment", "pr review 5 --comment -b hi", agent},
		{"pr review -c", "pr review 5 -c -b hi", agent},
		{"pr review --approve", "pr review 5 --approve", owner},
		{"pr review --request-changes", "pr review 5 --request-changes -b no", owner},
		{"pr review with no verdict", "pr review 5", owner},
		{"pr merge", "pr merge 5 --squash", owner},
		{"label create", "label create bug --color ff0000", owner},
		{"unknown command", "frobnicate now", owner},
		{"no command", "", owner},
		{"repo flag before command", "-R me/fork issue comment 5 -b hi", agent},
		{"issue close own", "issue close 7", agent},
		{"issue close own after repo flag", "issue close -R me/fork 7 --comment done", agent},
		{"issue close other author", "issue close 8", owner},
		{"issue close failed lookup", "issue close 404", owner},
		{"issue close without target", "issue close", owner},
		{"issue close two targets", "issue close 7 8", owner},
		{"issue reopen own", "issue reopen 7", agent},
		{"pr close own", "pr close 12", agent},
		{"pr close other author", "pr close 13 -c bye", owner},
		{"pr close current branch own", "pr close", agent},
		{"pr reopen other author", "pr reopen 13", owner},
		{"run rerun own", "run rerun 99", agent},
		{"run cancel other author", "run cancel 98", owner},
		{"repo fork upstream", "repo fork org/project", agent},
		{"repo fork upstream by URL", "repo fork https://github.com/org/project.git --clone=false", agent},
		{"repo fork upstream with host", "repo fork github.com/org/project", agent},
		{"repo fork other repository", "repo fork org/other", owner},
		{"repo fork upstream with --org", "repo fork org/project --org team", owner},
		{"repo fork own repository", "repo fork", owner},
		{"repo delete", "repo delete me/fork --yes", owner},
		{"api POST issue", "api repos/o/r/issues -f title=t", agent},
		{"api POST issue with leading slash", "api /repos/o/r/issues -X POST -f title=t", agent},
		{"api POST comment", "api repos/o/r/issues/5/comments -f body=hi", agent},
		{"api PATCH comment", "api repos/o/r/issues/comments/55 -X PATCH -f body=hi", agent},
		{"api POST pr", "api repos/o/r/pulls -f title=t -f head=h -f base=b", agent},
		{"api POST pr comment", "api repos/o/r/pulls/5/comments -f body=hi", agent},
		{"api PATCH issue title", "api repos/o/r/issues/8 -X PATCH -f title=t", agent},
		{"api PATCH issue close own", "api repos/o/r/issues/7 -X PATCH -f state=closed", agent},
		{"api PATCH issue close other author", "api repos/o/r/issues/8 -X PATCH -f state=closed", owner},
		{"api PATCH pr close own", "api repos/o/r/pulls/12 --method=PATCH --raw-field state=closed", agent},
		{"api PATCH pr close other author", "api repos/o/r/pulls/13 -X PATCH -fstate=closed", owner},
		{"api PATCH with unreadable body by other author", "api repos/o/r/issues/8 -X PATCH --input body.json", owner},
		{"api PATCH close in another repository", "api repos/other/repo/issues/7 -X PATCH -f state=closed", owner},
		{"api PUT pr merge", "api repos/o/r/pulls/5/merge -X PUT", owner},
		{"api POST pr merge", "api repos/o/r/pulls/5/merge -f merge_method=squash", owner},
		{"api POST review", "api repos/o/r/pulls/5/reviews -f event=APPROVE", owner},
		{"api DELETE", "api repos/o/r/issues/comments/55 -X DELETE", owner},
		{"api PUT", "api repos/o/r/collaborators/bob -X PUT", owner},
		{"api POST labels endpoint", "api repos/o/r/labels -f name=bug", owner},
		{"api POST outside repos", "api user/repos -f name=x", owner},
		{"api graphql mutation", "api graphql -f query=mutation{addStar(input:{starrableId:\"x\"}){clientMutationId}}", owner},
		{"api graphql POST without query", "api graphql -F id=1", owner},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(context.Background(), strings.Fields(tt.cmd), in); got != tt.want {
				t.Fatalf("Classify(%q) = %q, want %q", tt.cmd, got, tt.want)
			}
		})
	}
}

func TestClassifyWithoutLogin(t *testing.T) {
	in := ClassifyInput{Host: "github.com", Repo: "me/fork", AuthorOf: func(context.Context, ItemRef) (string, error) { return "x", nil }}
	if got := Classify(context.Background(), []string{"issue", "close", "7"}, in); got != approvals.ClassOwner {
		t.Fatalf("no room login gave %q, want owner", got)
	}
	in.Login = "x"
	in.AuthorOf = nil
	if got := Classify(context.Background(), []string{"issue", "close", "7"}, in); got != approvals.ClassOwner {
		t.Fatalf("no lookup gave %q, want owner", got)
	}
}
