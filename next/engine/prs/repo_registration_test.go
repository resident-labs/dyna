package prs

import (
	"net/http"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/prs/fakegithub"
	"github.com/resident-labs/dyna/engine/store"
)

func TestPollDiscardsOldRegistrationAndResetsItsMemory(t *testing.T) {
	ctx := t.Context()
	s, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	c, g, clock := client(t)
	const repo contract.RepoName = "owner/work"
	if e = s.CreateRoom(ctx, "r", "r", clock.Now()); e != nil {
		t.Fatal(e)
	}
	for _, r := range []contract.RepoName{"owner/default", repo} {
		if e = s.AddRoomRepo(ctx, "r", r, false); e != nil {
			t.Fatal(e)
		}
	}
	g.Add(fakegithub.Pull{Repo: repo, Number: 1})
	p := Poller{Store: s, GitHub: c, Now: clock.Now}
	if e = p.Pass(ctx); e != nil {
		t.Fatal(e)
	}
	original, err := s.RepoHeads(ctx, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	s.SetPRPollStatus(repo, original[repo].Registration, "old failure")
	g.SetPollInterval(int((15 * time.Minute) / time.Second))
	g.SetMain(repo, "old-poll-new-head")
	g.Edit(repo, 1, func(pr *fakegithub.Pull) { pr.Title = "old poll edit" })
	base := c.Transport
	ran := false
	c.Transport = pollTransport(func(r *http.Request) (*http.Response, error) {
		if !ran && r.URL.Path == "/repos/owner/work/git/ref/heads/main" {
			ran = true
			if e := s.RemoveRoomRepo(ctx, "r", repo); e != nil {
				t.Fatal(e)
			}
			if e := s.AddRoomRepo(ctx, "r", repo, false); e != nil {
				t.Fatal(e)
			}
			if status := s.PRPollStatus(ctx, repo); status != "" {
				t.Fatalf("old registration's status leaked: %q", status)
			}
		}
		return base.RoundTrip(r)
	})
	if e = p.Pass(ctx); e != nil {
		t.Fatal(e)
	}
	heads, e := s.RepoHeads(ctx, clock.Now())
	if e != nil {
		t.Fatal(e)
	}
	feeds, e := s.GitHubFeeds(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if !ran || heads[repo].Head != "" || len(feeds[repo]) > 0 {
		t.Fatalf("old active poll seeded re-added registration: ran=%t head=%q feeds=%d", ran, heads[repo].Head, len(feeds[repo]))
	}
	// The old poll left a long X-Poll-Interval and stamped its last-read time.
	// Low quota and that interval must not prevent the first read of the new registration.
	c.rest.note(100, 1, clock.Now().Add(time.Hour))
	g.SetMain(repo, "new-registration-head")
	if err := p.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.RepoHeads(ctx, clock.Now())
	if err != nil || fresh[repo].Head != "new-registration-head" {
		t.Fatalf("new registration inherited throttle: %+v %v", fresh[repo], err)
	}
}
