package discord

import (
	"context"
	"errors"
	"github.com/resident-labs/dyna/engine/discord/fakediscord"
	"sync/atomic"
	"testing"
	"time"
)

func TestReviewStoreFailureMustNotLoseInput(t *testing.T) {
	fake := fakediscord.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	var attempts atomic.Int32
	g := Gateway{Client: &Client{HTTP: fake.HTTP.Client(), Endpoint: fake.HTTP.URL, Token: fake.Token}, URL: fake.GatewayURL(), Bot: "900", Receive: func(ctx context.Context, m Message) error {
		if attempts.Add(1) == 1 {
			return errors.New("transient store write failure")
		}
		return nil
	}}
	fake.Events <- map[string]any{"id": "1", "channel_id": "201", "author": map[string]string{"id": "2"}}
	g.Run(ctx)
	if attempts.Load() != 2 {
		t.Fatalf("input lost after transient store failure: attempts %d", attempts.Load())
	}
}

func TestDiscordRetryExhaustionDropsThenContinues(t *testing.T) {
	fake := fakediscord.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 26*time.Second)
	defer cancel()
	type attempt struct {
		id string
		at time.Time
	}
	attempts := make(chan attempt, 6)
	g := Gateway{Client: &Client{HTTP: fake.HTTP.Client(), Endpoint: fake.HTTP.URL, Token: fake.Token}, URL: fake.GatewayURL(), Bot: "900", Receive: func(ctx context.Context, m Message) error {
		attempts <- attempt{m.ID, time.Now()}
		if m.ID == "1" {
			return errors.New("persistent store failure")
		}
		cancel()
		return nil
	}}
	for _, id := range []string{"1", "2"} {
		fake.Events <- map[string]any{"id": id, "channel_id": "201", "author": map[string]string{"id": "2"}}
	}
	_ = g.Run(ctx)
	if len(attempts) != 5 {
		t.Fatalf("got %d attempts", len(attempts))
	}
	previous := <-attempts
	if previous.id != "1" {
		t.Fatalf("unexpected first input %s", previous.id)
	}
	for _, delay := range []time.Duration{time.Second, 5 * time.Second, 15 * time.Second} {
		next := <-attempts
		if next.id != "1" || next.at.Sub(previous.at) < delay {
			t.Fatalf("retry %s after %s, wanted at least %s", next.id, next.at.Sub(previous.at), delay)
		}
		previous = next
	}
	if next := <-attempts; next.id != "2" {
		t.Fatalf("did not continue after exhaustion: %s", next.id)
	}
}

func TestDiscordRetryPreservesInputOrder(t *testing.T) {
	fake := fakediscord.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	attempts := make(chan string, 4)
	g := Gateway{Client: &Client{HTTP: fake.HTTP.Client(), Endpoint: fake.HTTP.URL, Token: fake.Token}, URL: fake.GatewayURL(), Bot: "900", Receive: func(ctx context.Context, m Message) error {
		attempts <- m.ID
		if m.ID == "1" && len(attempts) == 1 {
			return errors.New("transient store failure")
		}
		if m.ID == "2" {
			cancel()
		}
		return nil
	}}
	for _, id := range []string{"1", "2"} {
		fake.Events <- map[string]any{"id": id, "channel_id": "201", "author": map[string]string{"id": "2"}}
	}
	_ = g.Run(ctx)
	if len(attempts) != 3 {
		t.Fatalf("got %d attempts", len(attempts))
	}
	for _, expected := range []string{"1", "1", "2"} {
		if actual := <-attempts; actual != expected {
			t.Fatalf("got %s, wanted %s", actual, expected)
		}
	}
}
