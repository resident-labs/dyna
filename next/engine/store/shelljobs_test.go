package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestJobEndInheritsReply(t *testing.T) {
	for _, mode := range []string{"parent", "owner", "two", "open", "unnamed", "silent"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			channel := contract.ReplyChannel{Kind: contract.ReplyFinal, Target: endTurnParent, Home: endTurnFeed, ReplyTo: "input0"}
			if mode == "owner" {
				channel.Target = endTurnOwner
				channel.Home = endTurnDM
			}
			s, _ := endTurnFixture(t, []contract.ReplyChannel{channel}, contract.PostCreated{})
			jobs := []contract.ShellJobID{"j"}
			if mode == "two" || mode == "open" {
				jobs = append(jobs, "k")
			}
			if mode == "unnamed" {
				jobs = nil
			}
			end := contract.Event{ID: "end", Room: "room", From: endTurnChild, Time: time.Unix(1001, 0), Body: contract.TurnEnded{Session: "child", Turn: "turn", Outcome: contract.TurnCompleted, Jobs: jobs}}
			count := len(jobs)
			if mode == "unnamed" {
				count = 1 // Legacy agents report only the count.
			}
			if err := s.EndTurn(ctx, "child", contract.TurnOutputRequest{Turn: "turn", Outcome: contract.TurnCompleted, ShellJobs: count}, end); err != nil {
				t.Fatal(err)
			}
			if mode == "open" {
				start := contract.Event{ID: "start2", Room: "room", Time: time.Unix(1002, 0), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{}}
				if _, _, _, err := s.Dispatch(ctx, "child", "turn2", start, false); err != nil {
					t.Fatal(err)
				}
			}
			ends := []contract.ShellJobID{"j"}
			if mode == "two" || mode == "open" {
				ends = append(ends, "k")
			}
			for i, job := range ends {
				ev := contract.Event{ID: contract.EventID(fmt.Sprintf("job%d", i)), Room: "room", From: endTurnChild, Time: time.Unix(1003, 0), Body: contract.ShellJobEnded{Session: "child", Job: job}}
				if err := s.ShellJobEnded(ctx, "child", job, ev, false); err != nil {
					t.Fatal(err)
				}
				var kind contract.ReplyKind
				if err := s.Read().QueryRow("SELECT reply_kind FROM deliveries d JOIN events e ON e.seq=d.seq WHERE e.id=?", ev.ID).Scan(&kind); err != nil {
					t.Fatal(err)
				}
				want := contract.ReplyFinal
				if mode == "unnamed" || i < len(ends)-1 {
					want = contract.ReplyNone
				}
				if kind != want {
					t.Fatalf("job %s reply %s want %s", job, kind, want)
				}
			}
			if mode == "unnamed" || mode == "open" {
				return
			}
			start := contract.Event{ID: "start2", Room: "room", Time: time.Unix(1004, 0), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{}}
			if _, _, _, err := s.Dispatch(ctx, "child", "turn2", start, false); err != nil {
				t.Fatal(err)
			}
			final := "done"
			if mode == "silent" {
				final = ""
			}
			ev := contract.Event{ID: "end2", Room: "room", Time: time.Unix(1005, 0), From: endTurnChild, Body: contract.TurnEnded{Session: "child", Turn: "turn2", Outcome: contract.TurnCompleted, Final: final}}
			if err := s.EndTurn(ctx, "child", contract.TurnOutputRequest{Turn: "turn2", Outcome: contract.TurnCompleted, Final: final}, ev); err != nil {
				t.Fatal(err)
			}
			events, err := s.Events(ctx, "room", 0, 100, []contract.EventType{"turn.ended"})
			if err != nil {
				t.Fatal(err)
			}
			body := events[len(events)-1].Body.(contract.TurnEnded)
			if len(body.Replies) != 1 || body.Replies[0].Reply != channel {
				t.Fatalf("replies %+v", body.Replies)
			}
			if mode != "owner" {
				var wake bool
				if err := s.Read().QueryRow("SELECT wake FROM deliveries d JOIN events e ON e.seq=d.seq WHERE e.id='end2' AND d.to_id='parent'").Scan(&wake); err != nil || !wake {
					t.Fatalf("wake %v: %v", wake, err)
				}
			}
			if mode == "silent" {
				var n int
				if err := s.Read().QueryRow("SELECT COUNT(*) FROM checkins WHERE session='child' AND silent_turn IS NOT NULL").Scan(&n); err != nil || n != 0 {
					t.Fatalf("silent checkins %d: %v", n, err)
				}
			}
		})
	}
}
