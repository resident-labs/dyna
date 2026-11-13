package discord_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/engine/discord"
	"github.com/resident-labs/dyna/engine/discord/fakediscord"
)

func TestGatewayCountsServerEventsItCannotHold(t *testing.T) {
	fake := fakediscord.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	var lost atomic.Int64
	g := discord.Gateway{Client: &discord.Client{HTTP: fake.HTTP.Client(), Endpoint: fake.HTTP.URL, Token: fake.Token}, URL: fake.GatewayURL(), Bot: "900", Directory: &discord.Directory{}, Receive: func(context.Context, discord.Message) error { return nil },
		Observe: func(ctx context.Context, _ discord.ServerEvent) error { <-release; return nil }, Lost: &lost}
	go g.Run(ctx)
	defer close(release)
	for i := 0; i < 400; i++ {
		fake.Dispatches <- fakediscord.Dispatch{Type: "GUILD_MEMBER_ADD", Data: map[string]any{"guild_id": "100", "user": map[string]any{"id": "175928847299117063"}}}
	}
	deadline := time.Now().Add(5 * time.Second)
	for lost.Load() < 100 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	// One event is held by the blocked consumer and 256 wait, so 400 sent lose 143, or 144 if the consumer was slow to take the first.
	if got := lost.Load(); got < 143 || got > 144 {
		t.Fatalf("lost %d, want 143 or 144", got)
	}
}
