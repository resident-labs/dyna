package prs

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/prs/fakegithub"
	"github.com/resident-labs/dyna/engine/store"
)

func TestRoomPRMainFailureDoesNotPinEventsCursor(t *testing.T) {
	ctx := t.Context()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const repo contract.RepoName = "owner/repo"
	if err := s.CreateRoom(ctx, "room", "room", at); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if err := s.AddRoomRepo(ctx, "room", repo, true, &enabled); err != nil {
		t.Fatal(err)
	}
	c, server, clock := client(t)
	server.Add(fakegithub.Pull{Repo: repo, Number: 1, Draft: true})
	server.Add(fakegithub.Pull{Repo: repo, Number: 2, Draft: true})
	server.Edit(repo, 1, func(pr *fakegithub.Pull) { pr.Meta.Labels = []string{"seed"} }) // Seed the events cursor.
	p := Poller{Store: s, GitHub: c, Now: clock.Now}
	pass := func() {
		t.Helper()
		if err := p.Pass(ctx); err != nil {
			t.Fatal(err)
		}
	}
	feeds := func() map[string]store.GitHubFeed {
		t.Helper()
		all, err := s.GitHubFeeds(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return all[repo]
	}
	pass()
	before := feeds()["events"].Cursor.At
	// Only the events feed gaps. Churn on one PR fills more than 34 pages.
	server.Sleep(3 * time.Minute)
	for i := 0; i <= maxFeedPages*30; i++ {
		server.Edit(repo, 1, func(pr *fakegithub.Pull) { pr.Draft = !pr.Draft })
	}
	server.Add(fakegithub.Pull{Repo: repo, Number: 3})
	base := c.Transport
	c.Transport = pollTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/repos/owner/repo/git/ref/heads/main" {
			w := httptest.NewRecorder()
			w.WriteHeader(504)
			return w.Result(), nil
		}
		return base.RoundTrip(r)
	})
	pass()
	if got := feeds()["events"].Cursor.At; !got.After(before) {
		t.Fatalf("healthy events feed gap never committed: before=%s after=%s; status=%s", before, got, s.PRPollStatus(ctx, repo))
	}
}

func TestRoomPRCensusHasPageBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, repo := range []contract.RepoName{"a/b", "z/ok"} {
		if err := s.CreateRoom(ctx, contract.RoomID(repo), string(repo), at); err != nil {
			t.Fatal(err)
		}
		enabled := true
		if err := s.AddRoomRepo(ctx, contract.RoomID(repo), repo, true, &enabled); err != nil {
			t.Fatal(err)
		}
	}
	c, server, clock := client(t)
	server.Add(fakegithub.Pull{Repo: "a/b", Number: 1})
	server.Add(fakegithub.Pull{Repo: "z/ok", Number: 1})
	base := c.Transport
	pages := 0
	other := false
	c.Transport = pollTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/repos/z/ok/issues" {
			other = true
		}
		if r.URL.Path != "/repos/a/b/pulls" {
			return base.RoundTrip(r)
		}
		pages++
		if pages == 100 {
			cancel()
			return nil, ctx.Err()
		}
		w := httptest.NewRecorder()
		w.Header().Set("Link", fmt.Sprintf(`<%s?page=%d>; rel="next"`, r.URL.Path, pages+1))
		fmt.Fprint(w, `[{"number":1,"state":"open","updated_at":"2026-11-26T23:00:00Z"}]`)
		return w.Result(), nil
	})
	p := Poller{Store: s, GitHub: c, Now: clock.Now}
	if err := p.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if pages != maxFeedPages || !other {
		t.Fatalf("first repo consumed %d pages and healthy repo reached=%t", pages, other)
	}
}
func TestRoomPRRecoveryReconcilesKnownClosedPR(t *testing.T) {
	ctx := t.Context()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const repo contract.RepoName = "owner/repo"
	if err := s.CreateRoom(ctx, "room", "room", at); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if err := s.AddRoomRepo(ctx, "room", repo, true, &enabled); err != nil {
		t.Fatal(err)
	}
	c, server, clock := client(t)
	server.Add(fakegithub.Pull{Repo: repo, Number: 1, Draft: true})
	server.Add(fakegithub.Pull{Repo: repo, Number: 2, Draft: true})
	server.Edit(repo, 1, func(pr *fakegithub.Pull) { pr.Meta.Labels = []string{"seed"} }) // Seed the events cursor.
	p := Poller{Store: s, GitHub: c, Now: clock.Now}
	pass := func() {
		t.Helper()
		if err := p.Pass(ctx); err != nil {
			t.Fatal(err)
		}
	}
	feeds := func() map[string]store.GitHubFeed {
		t.Helper()
		all, err := s.GitHubFeeds(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return all[repo]
	}
	pass()
	// Only the events feed gaps. Churn on one PR fills more than 34 pages.
	server.Sleep(11 * time.Minute)
	clock.now = clock.now.Add(11 * time.Minute)
	server.Edit(repo, 2, func(pr *fakegithub.Pull) { pr.State = "CLOSED" })
	for i := 0; i <= maxFeedPages*30; i++ {
		server.Edit(repo, 1, func(pr *fakegithub.Pull) { pr.Draft = !pr.Draft })
	}
	server.Add(fakegithub.Pull{Repo: repo, Number: 3})
	base := c.Transport
	c.Transport = pollTransport(func(r *http.Request) (*http.Response, error) {
		// The issue snapshot is independently still cached (or the update also fell below its capped walk).
		if r.URL.Path == "/repos/owner/repo/issues" {
			w := httptest.NewRecorder()
			w.WriteHeader(304)
			return w.Result(), nil
		}
		return base.RoundTrip(r)
	})
	pass()
	if r := feeds()[feedRoomRecovery].Cursor.At; !r.IsZero() {
		t.Fatalf("unexpected pending recovery: %v", r)
	}
	// The reconciled snapshot must survive a restart.
	p = Poller{Store: s, GitHub: c, Now: clock.Now}
	pass()
	watches, err := s.RoomPRWatches(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if row := watches[0].Rows[2]; row.State != contract.PrClosed {
		t.Fatalf("recovery reported complete but known PR 2 stayed %s; status=%q", row.State, s.PRPollStatus(ctx, repo))
	}
}
func TestRoomPRCensusResumesAfterPageBudget(t *testing.T) {
	ctx := t.Context()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const repo contract.RepoName = "owner/repo"
	if err := s.CreateRoom(ctx, "room", "room", at); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if err := s.AddRoomRepo(ctx, "room", repo, true, &enabled); err != nil {
		t.Fatal(err)
	}
	c, server, clock := client(t)
	server.Add(fakegithub.Pull{Repo: repo, Number: 1})
	base := c.Transport
	last := 0
	c.Transport = pollTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/repos/owner/repo/pulls" {
			return base.RoundTrip(r)
		}
		p := 1
		fmt.Sscanf(r.URL.Query().Get("page"), "%d", &p)
		last = max(last, p)
		w := httptest.NewRecorder()
		if p < 35 {
			w.Header().Set("Link", fmt.Sprintf(`<%s?page=%d>; rel="next"`, r.URL.Path, p+1))
		}
		fmt.Fprint(w, `[{"number":1,"state":"open","updated_at":"2026-11-26T23:00:00Z"}]`)
		return w.Result(), nil
	})
	for i := 0; i < 3; i++ {
		p := Poller{Store: s, GitHub: c, Now: clock.Now}
		if err := p.Pass(ctx); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.RoomPRWatches(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if last < 35 || rows[0].CensusAt == 0 {
		t.Fatalf("three bounded passes (fresh pollers) reached page %d, census complete=%t, status=%s", last, rows[0].CensusAt != 0, s.PRPollStatus(ctx, repo))
	}
}
