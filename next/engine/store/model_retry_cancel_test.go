package store

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestModelRetryCancellation(t *testing.T) {
	for _, phase := range []string{"backoff", "queued", "running", "stop races failure", "closing"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			now := time.Unix(1000, 0)
			channel := contract.ReplyChannel{Kind: contract.ReplyNone}
			s, _ := endTurnFixture(t, []contract.ReplyChannel{channel}, contract.PostCreated{Text: "already sent tool post", Home: endTurnDM})
			if phase == "running" || phase == "stop races failure" {
				if stopping, err := s.RequestTurnStop(ctx, "child", now); err != nil || !stopping {
					t.Fatalf("stop running: %v %v", stopping, err)
				}
			}
			outcome, cause := contract.TurnFailed, contract.CauseModelFailed
			if phase == "running" {
				outcome, cause = contract.TurnStopped, ""
			}
			endChildTurn(t, s, contract.TurnOutputRequest{Outcome: outcome, Cause: cause})
			due := now.Add(5 * time.Minute)
			if phase == "queued" {
				if err := s.WakeModelRetries(ctx, due); err != nil {
					t.Fatal(err)
				}
				// A second due pass is read-only, not another retry or a timer clear.
				var before, after int
				if err := s.Read().QueryRow("SELECT model_retry_at FROM sessions WHERE id='child'").Scan(&before); err != nil {
					t.Fatal(err)
				}
				if err := s.WakeModelRetries(ctx, due); err != nil {
					t.Fatal(err)
				}
				if err := s.Read().QueryRow("SELECT model_retry_at FROM sessions WHERE id='child'").Scan(&after); err != nil || before != after || after == 0 {
					t.Fatalf("queued timer %d -> %d: %v", before, after, err)
				}
			}
			if phase == "backoff" || phase == "queued" {
				if stopping, err := s.RequestTurnStop(ctx, "child", due); err != nil || !stopping {
					t.Fatalf("stop %s: %v %v", phase, stopping, err)
				}
			}
			if phase == "closing" {
				event := contract.Event{ID: "close", Room: "room", Time: now, From: endTurnOwner, Body: contract.SessionClosed{Session: "child"}}
				if err := s.CloseSession(ctx, "child", event); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.WakeModelRetries(ctx, due.Add(24*time.Hour)); err != nil {
				t.Fatal(err)
			}
			var wakes int
			if err := s.Read().QueryRow("SELECT count(*) FROM deliveries WHERE to_id='child' AND state='pending' AND wake=1").Scan(&wakes); err != nil || wakes != 0 {
				t.Fatalf("%s revived %d inputs: %v", phase, wakes, err)
			}
			if phase != "closing" {
				var timer int64
				want := int64(-1)
				if phase == "running" {
					want = 0
				}
				if err := s.Read().QueryRow("SELECT model_retry_at FROM sessions WHERE id='child'").Scan(&timer); err != nil || timer != want {
					t.Fatalf("%s retained timer %d: %v", phase, timer, err)
				}
			}
			// Retrying/cancelling does not re-execute or delete unrelated tool effects.
			var posts int
			if err := s.Read().QueryRow("SELECT count(*) FROM events WHERE id='reply' AND type='post.created'").Scan(&posts); err != nil || posts != 1 {
				t.Fatalf("sent tool post count=%d: %v", posts, err)
			}
		})
	}
}
