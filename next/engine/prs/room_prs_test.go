package prs

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/prs/fakegithub"
	"github.com/resident-labs/dyna/engine/store"
	"testing"
	"time"
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
	if err := s.AddRoomRepo(ctx, "room", "owner/repo", true, true); err != nil {
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
	if err != nil || len(watches[0].Rows) != 102 {
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
