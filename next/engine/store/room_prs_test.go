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
	if err := s.AddRoomRepo(ctx, "room", "owner/repo", false, func() *bool { v := true; return &v }()); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRoomRepo(ctx, "room", "owner/repo", true, nil); err != nil {
		t.Fatal(err)
	}
	watches, err = s.RoomPRWatches(ctx, "owner/repo")
	if err != nil || len(watches) != 1 {
		t.Fatalf("enabled: %+v %v", watches, err)
	}
	pr := contract.PrSummary{Repo: "owner/repo", Number: 12, State: contract.PrOpen}
	pass := GitHubPass{RoomPRs: []RoomPRPass{{Room: "room", Repo: pr.Repo, CensusComplete: true, Rows: []TrackedPR{{PrSummary: pr}}, Changes: []contract.PrChange{{PR: pr, Kinds: []contract.PrChangeKind{contract.PrChangeReady}}}}}}
	if err := s.ApplyPRPoll(ctx, pass, time.Now()); err != nil {
		t.Fatal(err)
	}
	watches, err = s.RoomPRWatches(ctx, pr.Repo)
	if err != nil || len(watches[0].Rows) != 1 || watches[0].CensusAt == 0 {
		t.Fatalf("snapshots: %+v %v", watches, err)
	}
	var count int
	if err := s.Read().QueryRowContext(ctx, `SELECT count(*) FROM events WHERE room='room' AND type='github.pr.changed'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("untracked event: %d %v", count, err)
	}
	if err := s.AddRoomRepo(ctx, "room", pr.Repo, false, func() *bool { v := false; return &v }()); err != nil {
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
	if err := s.AddRoomRepo(ctx, "room", "owner/repo", true, func() *bool { v := true; return &v }()); err != nil {
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

func TestRoomPRCensusLifecycle(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRoom(ctx, "room", "room", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRoomRepo(ctx, "room", "owner/repo", true, func() *bool { v := true; return &v }()); err != nil {
		t.Fatal(err)
	}
	check := func(want int64) {
		t.Helper()
		watches, err := s.RoomPRWatches(ctx, "owner/repo")
		if err != nil || len(watches) != 1 {
			t.Fatalf("watches: %+v %v", watches, err)
		}
		if watches[0].CensusAt != want || len(watches[0].Rows) != 0 {
			t.Fatalf("empty census: %+v", watches[0])
		}
	}
	check(0)
	pass := GitHubPass{RoomPRs: []RoomPRPass{{Room: "room", Repo: "owner/repo", CensusComplete: true}}}
	at := time.UnixMilli(12345)
	if err := s.ApplyPRPoll(ctx, pass, at); err != nil {
		t.Fatal(err)
	}
	check(at.UnixMilli())
	if err := s.ApplyPRPoll(ctx, pass, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	check(at.UnixMilli())
	// Updating the default or opting in again must preserve the completed census.
	if err := s.AddRoomRepo(ctx, "room", "owner/repo", false, func() *bool { v := true; return &v }()); err != nil {
		t.Fatal(err)
	}
	check(at.UnixMilli())
	if err := s.AddRoomRepo(ctx, "room", "owner/repo", false, func() *bool { v := false; return &v }()); err != nil {
		t.Fatal(err)
	}
	// An in-flight pass cannot complete a disabled census.
	if err := s.ApplyPRPoll(ctx, pass, at); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRoomRepo(ctx, "room", "owner/repo", false, func() *bool { v := true; return &v }()); err != nil {
		t.Fatal(err)
	}
	check(0)
}
