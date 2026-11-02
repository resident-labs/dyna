package prs

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/prs/fakegithub"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func client(t *testing.T) (*Client, *fakegithub.Server, *clock) {
	t.Helper()
	server := fakegithub.New(t)
	at := &clock{now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	return &Client{Token: fakegithub.Token, Endpoint: server.Endpoint(), Transport: http.DefaultTransport, Now: at.Now}, server, at
}

func TestPullSendsETagAndReusesTheAnswer(t *testing.T) {
	c, server, _ := client(t)
	ctx := context.Background()
	server.Add(fakegithub.Pull{Repo: "owner/repo", Number: 4, Title: "first"})
	first, err := c.Pull(ctx, "owner/repo", 4)
	if err != nil || first.Title != "first" || first.Head != "head1" {
		t.Fatalf("%+v %v", first, err)
	}
	if sent := server.SentETags(); len(sent) != 0 {
		t.Fatalf("first read sent %v", sent)
	}
	second, err := c.Pull(ctx, "owner/repo", 4)
	if err != nil || !reflect.DeepEqual(second, first) {
		t.Fatalf("%+v %v", second, err)
	}
	if _, rest, notModified := server.Counts(); rest != 2 || notModified != 1 {
		t.Fatalf("rest %d, 304 %d", rest, notModified)
	}
	server.Edit("owner/repo", 4, func(p *fakegithub.Pull) { p.Title = "second" })
	third, err := c.Pull(ctx, "owner/repo", 4)
	if err != nil || third.Title != "second" {
		t.Fatalf("%+v %v", third, err)
	}
	if sent := server.SentETags(); len(sent) != 2 {
		t.Fatalf("sent %v", sent)
	}
}

func TestPullMissing(t *testing.T) {
	c, _, _ := client(t)
	if _, err := c.Pull(context.Background(), "owner/repo", 4); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := c.Pull(context.Background(), "owner/../repo", 4); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestReadAsksOnceForEveryPR(t *testing.T) {
	c, server, _ := client(t)
	var numbers []int
	for number := 1; number <= 20; number++ {
		server.Add(fakegithub.Pull{Repo: "owner/repo", Number: number})
		numbers = append(numbers, number)
	}
	pulls, err := c.Read(context.Background(), "owner/repo", numbers)
	if err != nil || len(pulls) != 20 || c.Viewer() != "owner-account" {
		t.Fatalf("%+v %v", pulls, err)
	}
	graphql, rest, _ := server.Counts()
	if sizes := server.QuerySizes(); graphql != 1 || rest != 0 || len(sizes) != 1 || sizes[0] != 20 {
		t.Fatalf("graphql %d rest %d sizes %v", graphql, rest, sizes)
	}
}

func TestReadWithNothingToAskMakesNoQuery(t *testing.T) {
	c, server, _ := client(t)
	if _, err := c.Read(context.Background(), "owner/repo", nil); err != nil {
		t.Fatal(err)
	}
	if graphql, _, _ := server.Counts(); graphql != 0 {
		t.Fatalf("%d queries", graphql)
	}
}

func TestReadKeepsTheOthersWhenOnePRIsGone(t *testing.T) {
	c, server, _ := client(t)
	server.Add(fakegithub.Pull{Repo: "owner/repo", Number: 1})
	pulls, err := c.Read(context.Background(), "owner/repo", []int{1, 2})
	if err != nil || pulls[1] == nil || len(pulls) != 1 {
		t.Fatalf("%+v %v", pulls, err)
	}
}

func TestReadTellsReviewsWhereNoBranchRuleDecides(t *testing.T) {
	c, server, _ := client(t)
	server.Add(fakegithub.Pull{Repo: "owner/repo", Number: 1, LatestReviews: []string{"APPROVED", "CHANGES_REQUESTED"}})
	server.Add(fakegithub.Pull{Repo: "owner/repo", Number: 2, LatestReviews: []string{"APPROVED"}})
	server.Add(fakegithub.Pull{Repo: "owner/repo", Number: 3, ReviewDecision: "REVIEW_REQUIRED", LatestReviews: []string{"APPROVED"}})
	pulls, err := c.Read(context.Background(), "owner/repo", []int{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	for number, want := range map[int]contract.PrReview{1: contract.PrChangesRequested, 2: contract.PrApproved, 3: contract.PrReviewRequired} {
		if got := pulls[number].Review; got != want {
			t.Errorf("PR %d: %q, want %q", number, got, want)
		}
	}
}

func TestViewerLoadsOnceAndAQuietClientAsksNoMore(t *testing.T) {
	c, server, _ := client(t)
	for range 3 {
		if err := c.LoadViewer(context.Background()); err != nil || c.Viewer() != "owner-account" {
			t.Fatalf("%q %v", c.Viewer(), err)
		}
	}
	if graphql, _, _ := server.Counts(); graphql != 1 {
		t.Fatalf("%d queries", graphql)
	}
}

func TestLowQuotaHoldsUntilResetOrMoreRoom(t *testing.T) {
	c, server, at := client(t)
	ctx := context.Background()
	server.Add(fakegithub.Pull{Repo: "owner/repo", Number: 1})
	reset := at.Now().Add(30 * time.Minute)
	server.SetQuota(5000, 999, reset)
	if _, err := c.Read(ctx, "owner/repo", []int{1}); err != nil {
		t.Fatal(err)
	}
	if until, low := c.Low(); !low || !until.Equal(reset) {
		t.Fatalf("low %v until %v", low, until)
	}
	server.SetQuota(5000, 1000, reset)
	if _, err := c.Read(ctx, "owner/repo", []int{1}); err != nil {
		t.Fatal(err)
	}
	if _, low := c.Low(); low {
		t.Fatal("exactly 20% left is not below the floor")
	}
	server.SetQuota(5000, 1, reset)
	if _, err := c.Read(ctx, "owner/repo", []int{1}); err != nil {
		t.Fatal(err)
	}
	at.add(31 * time.Minute)
	if _, low := c.Low(); low {
		t.Fatal("low outlived the reset")
	}
}

func TestRefusalPausesUntilGitHubsWaitEnds(t *testing.T) {
	c, server, at := client(t)
	ctx := context.Background()
	server.Add(fakegithub.Pull{Repo: "owner/repo", Number: 1})
	server.RefuseFor(120)
	if _, err := c.Read(ctx, "owner/repo", []int{1}); !errors.Is(err, ErrPaused) {
		t.Fatal(err)
	}
	server.RefuseFor(0)
	if _, err := c.Read(ctx, "owner/repo", []int{1}); !errors.Is(err, ErrPaused) {
		t.Fatalf("sent while paused: %v", err)
	}
	if graphql, _, _ := server.Counts(); graphql != 0 {
		t.Fatalf("%d queries reached GitHub", graphql)
	}
	at.add(121 * time.Second)
	if _, err := c.Read(ctx, "owner/repo", []int{1}); err != nil {
		t.Fatal(err)
	}
}

func TestBadTokenReportsNoToken(t *testing.T) {
	c, server, _ := client(t)
	c.Token = "not-the-token"
	server.Add(fakegithub.Pull{Repo: "owner/repo", Number: 1})
	_, err := c.Read(context.Background(), "owner/repo", []int{1})
	if err == nil || err.Error() != "GitHub HTTP 401" {
		t.Fatalf("%v", err)
	}
}
