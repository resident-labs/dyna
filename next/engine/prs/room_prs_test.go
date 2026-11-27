package prs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/prs/fakegithub"
	"github.com/resident-labs/dyna/engine/store"
)

func TestRoomPRCensusAndUntrackedChanges(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRoom(ctx, "room", "room", at); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRoomRepo(ctx, "room", "owner/repo", true, store.RoomRepoOptions{PREvents: new(true)}); err != nil {
		t.Fatal(err)
	}
	c, server, clock := client(t)
	// More than one REST page, with no session tracking any of them.
	for n := 1; n <= 101; n++ {
		server.Add(fakegithub.Pull{Repo: "owner/repo", Number: n, Title: fmt.Sprint(n), Author: "writer", Rollup: "SUCCESS"})
	}
	p := Poller{Store: s, GitHub: c, Now: clock.Now}
	pass := func() {
		t.Helper()
		if err := p.Pass(ctx); err != nil {
			t.Fatal(err)
		}
	}
	count := func() int {
		t.Helper()
		var n int
		if err := s.Read().QueryRowContext(ctx, `SELECT count(*) FROM events WHERE type='github.pr.changed'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	pass()
	watches, err := s.RoomPRWatches(ctx, "owner/repo")
	if err != nil || len(watches[0].Rows) != 101 || watches[0].CensusAt == 0 {
		t.Fatalf("census: %d %v", len(watches[0].Rows), err)
	}
	if count() != 0 {
		t.Fatal("census was not silent")
	}
	clock.add(11 * time.Minute)
	pass() // settle the main CI, if necessary
	gql, _, _ := server.Counts()
	pass()
	after, _, _ := server.Counts()
	if after != gql {
		t.Fatalf("quiet census reread details: %d queries", after-gql)
	}
	server.EditAs("writer", "owner/repo", 101, func(pr *fakegithub.Pull) { pr.Draft = true })
	pass()
	if count() != 1 {
		t.Fatalf("untracked draft events: %d", count())
	}
	var data []byte
	if err := s.Read().QueryRowContext(ctx, `SELECT body FROM events WHERE type='github.pr.changed' ORDER BY seq DESC LIMIT 1`).Scan(&data); err != nil {
		t.Fatal(err)
	}
	var event contract.PrChanged
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	if len(event.Changes) != 1 || event.Changes[0].PR.Number != 101 {
		t.Fatalf("%+v", event)
	}
	server.Add(fakegithub.Pull{Repo: "owner/repo", Number: 102, Title: "new", Author: "writer", Rollup: "SUCCESS"})
	pass()
	if count() != 2 {
		t.Fatalf("new opening events: %d", count())
	}
	pass()
	if count() != 2 {
		t.Fatal("quiet pass repeated event")
	}
}

func TestUntrackedRoomCISettlesOnce(t *testing.T) {
	for _, result := range []string{"SUCCESS", "FAILURE", "CANCELLED"} {
		t.Run(result, func(t *testing.T) {
			ctx := context.Background()
			s, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := s.CreateRoom(ctx, "room", "room", at); err != nil {
				t.Fatal(err)
			}
			if err := s.AddRoomRepo(ctx, "room", "owner/repo", true, store.RoomRepoOptions{PREvents: new(true)}); err != nil {
				t.Fatal(err)
			}
			c, server, clock := client(t)
			server.Add(fakegithub.Pull{Repo: "owner/repo", Number: 1, Author: "writer", Rollup: "PENDING", Checks: []fakegithub.Check{{Name: "build", Status: "IN_PROGRESS"}}})
			p := Poller{Store: s, GitHub: c, Now: clock.Now}
			pass := func() {
				t.Helper()
				if err := p.Pass(ctx); err != nil {
					t.Fatal(err)
				}
			}
			count := func() int {
				t.Helper()
				var n int
				if err := s.Read().QueryRowContext(ctx, `SELECT count(*) FROM events WHERE type='github.ci.finished'`).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			pass()
			if count() != 0 {
				t.Fatal("baseline emitted CI")
			}
			server.Edit("owner/repo", 1, func(pr *fakegithub.Pull) {
				pr.Rollup = result
				pr.Checks = []fakegithub.Check{{Name: "build", Status: "COMPLETED", Conclusion: result}}
			})
			pass()
			if count() != 1 {
				t.Fatalf("settled CI events: %d", count())
			}
			pass()
			if count() != 1 {
				t.Fatal("settled CI emitted twice")
			}
			var data []byte
			if err := s.Read().QueryRowContext(ctx, `SELECT body FROM events WHERE type='github.ci.finished'`).Scan(&data); err != nil {
				t.Fatal(err)
			}
			var event contract.CIFinished
			if err := json.Unmarshal(data, &event); err != nil {
				t.Fatal(err)
			}
			want := contract.PrCISuccess
			if result == "FAILURE" {
				want = contract.PrCIFailure
			}
			if result == "CANCELLED" {
				want = contract.PrCICancelled
			}
			if event.Repo != "owner/repo" || event.Number != 1 || event.Result != want || event.Head == "" || event.Session != "" {
				t.Fatalf("%+v", event)
			}
			var deliveries int
			if err := s.Read().QueryRowContext(ctx, `SELECT count(*) FROM deliveries`).Scan(&deliveries); err != nil || deliveries != 0 {
				t.Fatalf("direct deliveries: %d %v", deliveries, err)
			}
		})
	}
}

type pollTransport func(*http.Request) (*http.Response, error)

func (f pollTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRoomPRGapKeepsLiveEventsAndRetriesRecovery(t *testing.T) {
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
	if err := s.AddRoomRepo(ctx, "room", repo, true, store.RoomRepoOptions{PREvents: &enabled}); err != nil {
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
	var listAttempts, detailAttempts int
	fail := true
	base := c.Transport
	c.Transport = pollTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/graphql":
			if fail {
				detailAttempts++
				w := httptest.NewRecorder()
				w.WriteHeader(http.StatusGatewayTimeout)
				return w.Result(), nil
			}
		case "/repos/owner/repo/pulls":
			listAttempts++
			if fail {
				w := httptest.NewRecorder()
				status := http.StatusGatewayTimeout
				if listAttempts == 1 {
					status = http.StatusBadGateway
				}
				w.WriteHeader(status)
				return w.Result(), nil
			}
		case "/repos/owner/repo/issues":
			if !fail {
				// Simulate a feed whose ETag has not caught up; only recovery can see PR 2's push.
				w := httptest.NewRecorder()
				w.WriteHeader(http.StatusNotModified)
				return w.Result(), nil
			}
		}
		return base.RoundTrip(r)
	})
	pass()
	after := feeds()
	if listAttempts != 3 || detailAttempts != 3 || !after["events"].Cursor.At.After(before) || after[feedRoomRecovery].Cursor.At.IsZero() {
		t.Fatalf("list attempts=%d, detail attempts=%d, events at=%s (was %s), recovery=%s", listAttempts, detailAttempts, after["events"].Cursor.At, before, after[feedRoomRecovery].Cursor.At)
	}
	if status := s.PRPollStatus(ctx, repo); status != "degraded: PR list: GitHub HTTP 504; PR details: GitHub HTTP 504" {
		t.Fatalf("status=%q", status)
	}
	latest := func(n int, kind contract.PrChangeKind) {
		t.Helper()
		var data []byte
		if err := s.Read().QueryRowContext(ctx, `SELECT body FROM events WHERE type='github.pr.changed' ORDER BY seq DESC LIMIT 1`).Scan(&data); err != nil {
			t.Fatal(err)
		}
		var changed contract.PrChanged
		if err := json.Unmarshal(data, &changed); err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(changed.Changes, func(c contract.PrChange) bool { return c.PR.Number == n && slices.Contains(c.Kinds, kind) }) {
			t.Fatalf("missing PR %d %s in %s", n, kind, data)
		}
	}
	latest(3, contract.PrChangeOpened)
	latest(1, contract.PrChangeReady)
	fail = false
	p = Poller{Store: s, GitHub: c, Now: clock.Now} // Pending recovery survives a poller restart.
	server.Edit(repo, 2, func(pr *fakegithub.Pull) { pr.Head = "new-head" })
	pass()
	latest(2, contract.PrChangePushed)
	if status := s.PRPollStatus(ctx, repo); status != "" || !feeds()[feedRoomRecovery].Cursor.At.IsZero() || listAttempts != 4 {
		t.Fatalf("recovery did not clear: status=%q, list attempts=%d", status, listAttempts)
	}
}
