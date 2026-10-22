package questions

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDeliveryWorkerDrainsSavedAnswers(t *testing.T) {
	s := &Store{Root: questionRoot(t)}
	q, err := s.Ask(sample("a"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Answer(q.Room, q.ID, Answer{By: "owner", Choice: "yes"}); err != nil {
		t.Fatal(err)
	}
	called := 0
	retry, err := s.Tick(context.Background(), time.Now(), func(ctx context.Context, room string) error {
		called++
		if room != q.Room {
			t.Fatal(room)
		}
		return s.MarkDelivered(room, q.ID)
	})
	if err != nil || called != 1 || !retry.IsZero() {
		t.Fatal(called, retry, err)
	}
	if retry, err = s.Tick(context.Background(), time.Now(), func(context.Context, string) error { t.Fatal("idle worker sent"); return nil }); err != nil || !retry.IsZero() {
		t.Fatal(retry, err)
	}
}

// A failed delivery stays pending, and Tick asks to be woken a minute later
// to retry it.
func TestDeliveryTickRetriesFailuresAfterAMinute(t *testing.T) {
	s := &Store{Root: questionRoot(t)}
	q, err := s.Ask(sample("a"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Answer(q.Room, q.ID, Answer{By: "owner", Choice: "yes"}); err != nil {
		t.Fatal(err)
	}
	failed := errors.New("post failed")
	calls := 0
	now := time.Now()
	retry, err := s.Tick(context.Background(), now, func(context.Context, string) error { calls++; return failed })
	if !errors.Is(err, failed) {
		t.Fatal(err)
	}
	if !retry.Equal(now.Add(retryAfter)) {
		t.Fatalf("retry at %v, want %v", retry, now.Add(retryAfter))
	}
	if calls != 1 {
		t.Fatalf("tick retried %d times", calls)
	}
	rooms, err := s.PendingRooms()
	if err != nil || len(rooms) != 1 {
		t.Fatal(rooms, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Tick(ctx, time.Now(), func(context.Context, string) error { t.Fatal("cancelled tick sent an answer"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
