package prs

import (
	"context"
	"errors"
	"testing"

	"github.com/resident-labs/dyna/engine/prs/fakegithub"
)

func TestATrackingReadCarriesMetadataSorted(t *testing.T) {
	c, server, _ := client(t)
	meta := fakegithub.Meta{Labels: []string{"z", "a"}, Assignees: []string{"y", "x"}, Milestone: "v2", Body: "text"}
	server.Add(fakegithub.Pull{Repo: "owner/repo", Number: 1, Draft: true, Meta: meta})
	server.AddIssue(fakegithub.Issue{Repo: "owner/repo", Number: 2, Title: "bug", State: "CLOSED", Meta: meta})
	pull, err := c.Pull(context.Background(), "owner/repo", 1)
	if err != nil || pull.Meta.Labels[0] != "a" || pull.Meta.Assignees[0] != "x" || !pull.Draft || pull.Meta.Milestone != "v2" || pull.Meta.Body == "" {
		t.Fatalf("%+v %v", pull, err)
	}
	repo, issue, err := c.Issue(context.Background(), "owner/repo", 2)
	if err != nil || repo != "owner/repo" || issue.State != "closed" || issue.Title != "bug" || issue.Meta.Labels[1] != "z" || issue.Meta.Body != pull.Meta.Body {
		t.Fatalf("%q %+v %v", repo, issue, err)
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
