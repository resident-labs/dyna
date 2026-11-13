package store

import (
	"context"
	"github.com/resident-labs/dyna/contract"
	"testing"
	"time"
)

func TestRoomPREventsOptIn(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRoom(ctx, "room", "room", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRoomRepo(ctx, "room", "owner/repo", false); err != nil {
		t.Fatal(err)
	}
	watches, err := s.RoomPRWatches(ctx, "owner/repo")
	if err != nil || len(watches) != 0 {
		t.Fatalf("default: %+v %v", watches, err)
	}
	if err := s.AddRoomRepo(ctx, "room", "owner/repo", false, true); err != nil {
		t.Fatal(err)
	}
	watches, err = s.RoomPRWatches(ctx, "owner/repo")
	if err != nil || len(watches) != 1 {
		t.Fatalf("enabled: %+v %v", watches, err)
	}
	pr := contract.PrSummary{Repo: "owner/repo", Number: 12, State: contract.PrOpen}
	pass := GitHubPass{RoomPRs: []RoomPRPass{{Room: "room", Repo: pr.Repo, Rows: []TrackedPR{{}, {PrSummary: pr}}, Changes: []contract.PrChange{{PR: pr, Kinds: []contract.PrChangeKind{contract.PrChangeReady}}}}}}
	if err := s.ApplyPRPoll(ctx, pass, time.Now()); err != nil {
		t.Fatal(err)
	}
	watches, err = s.RoomPRWatches(ctx, pr.Repo)
	if err != nil || len(watches[0].Rows) != 2 {
		t.Fatalf("snapshots: %+v %v", watches, err)
	}
	var count int
	if err := s.Read().QueryRowContext(ctx, `SELECT count(*) FROM events WHERE room='room' AND type='github.pr.changed'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("untracked event: %d %v", count, err)
	}
	if err := s.AddRoomRepo(ctx, "room", pr.Repo, false, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Read().QueryRowContext(ctx, `SELECT count(*) FROM room_pr_observed`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("disable clears baseline: %d %v", count, err)
	}
}

func TestTrackedRoomCIIsNotDuplicated(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRoom(ctx, "room", "room", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRoomRepo(ctx, "room", "owner/repo", true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.ExecContext(ctx, `INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','room','s','p','ready','',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.ExecContext(ctx, `INSERT INTO pr_owners(repo,number,session,goal,approved_head) VALUES('owner/repo',1,'s','','')`); err != nil {
		t.Fatal(err)
	}
	pr := contract.PrSummary{Repo: "owner/repo", Number: 1, State: contract.PrOpen, Head: "head", CI: contract.PrCISuccess}
	change := contract.PrChange{PR: pr, Kinds: []contract.PrChangeKind{contract.PrChangeCIPassed}}
	pass := GitHubPass{PRs: []PRUpdate{{PR: pr, Was: contract.PrOpen, Change: &change}}, RoomPRs: []RoomPRPass{{Room: "room", Repo: pr.Repo, Rows: []TrackedPR{{PrSummary: pr}}, Changes: []contract.PrChange{change}}}}
	if err := s.ApplyPRPoll(ctx, pass, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"github.pr.changed", "github.ci.finished"} {
		var n int
		if err := s.Read().QueryRowContext(ctx, `SELECT count(*) FROM events WHERE type=? AND room='room'`, kind).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s: %d %v", kind, n, err)
		}
	}
}
