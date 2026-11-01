package prs

import (
	"context"
	"errors"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/prs/fakegithub"
)

func TestPollAsksOnceForPRsAndIssuesTogether(t *testing.T) {
	c, server, _ := client(t)
	var pulls, issues []int
	for number := 1; number <= 20; number++ {
		server.Add(fakegithub.Pull{Repo: "owner/repo", Number: number})
		server.AddIssue(fakegithub.Issue{Repo: "owner/repo", Number: 100 + number})
		pulls, issues = append(pulls, number), append(issues, 100+number)
	}
	snapshot, err := c.Poll(context.Background(), "owner/repo", pulls, issues)
	if err != nil || len(snapshot.PRs) != 20 || len(snapshot.Issues) != 20 || snapshot.Repo != "owner/repo" {
		t.Fatalf("%+v %v", snapshot, err)
	}
	if graphql, rest, _ := server.Counts(); graphql != 1 || rest != 0 {
		t.Fatalf("graphql %d rest %d", graphql, rest)
	}
	if sizes, issueSizes := server.QuerySizes(), server.IssueQuerySizes(); sizes[0] != 20 || issueSizes[0] != 20 {
		t.Fatalf("sizes %v %v", sizes, issueSizes)
	}
}

func TestPollReadsMetadataSorted(t *testing.T) {
	c, server, _ := client(t)
	meta := fakegithub.Meta{Labels: []string{"z", "a"}, Assignees: []string{"y", "x"}, Milestone: "v2", EditedAt: "2026-11-01T12:00:00Z", Reopens: 2}
	server.Add(fakegithub.Pull{Repo: "owner/repo", Number: 1, Draft: true, Meta: meta, Reviewers: []string{"r2", "r1"}})
	server.AddIssue(fakegithub.Issue{Repo: "owner/repo", Number: 2, Title: "bug", State: "CLOSED", Meta: meta, Comments: []fakegithub.Comment{{ID: 5, Author: "x", Body: "hi"}}})
	snapshot, err := c.Poll(context.Background(), "owner/repo", []int{1}, []int{2})
	if err != nil {
		t.Fatal(err)
	}
	pull, issue := snapshot.PRs[1].Meta, snapshot.Issues[2]
	if pull.Labels[0] != "a" || pull.Assignees[0] != "x" || pull.Reviewers[0] != "r1" || !pull.Draft || pull.Milestone != "v2" || pull.Reopened != 2 || pull.EditedAt == "" {
		t.Fatalf("%+v", pull)
	}
	if issue.State != contract.IssueClosed || issue.Meta.Title != "bug" || issue.Meta.Labels[1] != "z" || len(issue.Comments) != 1 || issue.Comments[0].ID != 5 {
		t.Fatalf("%+v", issue)
	}
}

func TestPollWithOnlyIssuesSendsNoPullFragment(t *testing.T) {
	c, server, _ := client(t)
	server.AddIssue(fakegithub.Issue{Repo: "owner/repo", Number: 1})
	if snapshot, err := c.Poll(context.Background(), "owner/repo", nil, []int{1}); err != nil || len(snapshot.Issues) != 1 {
		t.Fatalf("%+v %v", snapshot, err)
	}
}

func TestIssueReadFindsNothingAtAPRsNumberOrInAMissingRepository(t *testing.T) {
	c, server, _ := client(t)
	ctx := context.Background()
	server.Add(fakegithub.Pull{Repo: "owner/repo", Number: 4})
	server.AddIssue(fakegithub.Issue{Repo: "owner/repo", Number: 5})
	if repo, issue, err := c.Issue(ctx, "owner/repo", 5); err != nil || repo != "owner/repo" || issue.URL == "" {
		t.Fatalf("%q %+v %v", repo, issue, err)
	}
	if _, _, err := c.Issue(ctx, "owner/repo", 4); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, _, err := c.Issue(ctx, "owner/../repo", 5); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	server.HideRepo("owner/repo")
	if _, _, err := c.Issue(ctx, "owner/repo", 5); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
