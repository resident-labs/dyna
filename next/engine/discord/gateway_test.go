package discord_test

import (
	"context"
	"github.com/resident-labs/dyna/engine/discord"
	"github.com/resident-labs/dyna/engine/discord/fakediscord"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func TestGatewayFiltersHumansAndMembership(t *testing.T) {
	fake := fakediscord.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	received := make(chan discord.Message, 8)
	g := discord.Gateway{Client: &discord.Client{HTTP: fake.HTTP.Client(), Endpoint: fake.HTTP.URL, Token: fake.Token}, URL: fake.GatewayURL(), Bot: "900", Directory: &discord.Directory{}, Receive: func(ctx context.Context, m discord.Message) error { received <- m; return nil }}
	done := make(chan struct{})
	go func() { defer close(done); g.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("gateway did not stop")
		}
	}()
	for _, event := range []map[string]any{
		{"id": "bot", "channel_id": "200", "guild_id": "100", "author": map[string]any{"id": "2", "bot": true}, "mentions": []map[string]string{{"id": "900"}}},
		{"id": "self", "channel_id": "200", "guild_id": "100", "author": map[string]any{"id": "900"}, "mentions": []map[string]string{{"id": "900"}}},
		{"id": "unaddressed", "channel_id": "200", "guild_id": "100", "author": map[string]any{"id": "1"}},
		{"id": "role", "channel_id": "200", "guild_id": "100", "author": map[string]any{"id": "1"}, "mention_roles": []string{"11"}},
		{"id": "role-text", "channel_id": "200", "guild_id": "100", "author": map[string]any{"id": "1"}, "content": "<@&11> who should I wait for?"},
		{"id": "code-role", "channel_id": "200", "guild_id": "100", "author": map[string]any{"id": "1"}, "content": "```\n<@&11>\n``` and `<@&11>`"},
		{"id": "span-role", "channel_id": "200", "guild_id": "100", "author": map[string]any{"id": "1"}, "content": "``a ` <@&11>`` and ```\nx`y <@&11>\n```"},
		{"id": "escaped-role", "channel_id": "200", "guild_id": "100", "author": map[string]any{"id": "1"}, "content": "type \\<@&11> to ping it"},
		{"id": "escaped-then-role", "channel_id": "200", "guild_id": "100", "author": map[string]any{"id": "1"}, "content": "type \\<@&11> to ping it; <@&11> who should I wait for?"},
		{"id": "escaped-backtick", "channel_id": "200", "guild_id": "100", "author": map[string]any{"id": "1"}, "content": "\\`example <@&11>\\` who?"},
		{"id": "escaped-backslash", "channel_id": "200", "guild_id": "100", "author": map[string]any{"id": "1"}, "content": "a\\\\<@&11>"},
		{"id": "other-role", "channel_id": "200", "guild_id": "100", "author": map[string]any{"id": "1"}, "mention_roles": []string{"10"}},
		{"id": "mention", "channel_id": "200", "guild_id": "100", "author": map[string]any{"id": "1"}, "mentions": []map[string]string{{"id": "900"}}},
		{"id": "reply", "channel_id": "200", "guild_id": "100", "author": map[string]any{"id": "1"}, "referenced_message": map[string]any{"author": map[string]string{"id": "900"}}},
		{"id": "dm", "channel_id": "201", "author": map[string]any{"id": "1"}},
	} {
		fake.Events <- event
	}
	for _, want := range []string{"role", "role-text", "escaped-then-role", "escaped-backtick", "escaped-backslash", "mention", "reply", "dm"} {
		select {
		case got := <-received:
			if got.ID != want {
				t.Fatalf("got %s want %s", got.ID, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("gateway did not deliver", want)
		}
	}
}
func TestSplit(t *testing.T) {
	for _, input := range []string{strings.Repeat("x", 6000), "```go\n" + strings.Repeat("x\n", 3000) + "```", strings.Repeat("😀", 2500)} {
		chunks := discord.Split(input)
		if len(chunks) < 2 {
			t.Fatal("not split")
		}
		for _, chunk := range chunks {
			if len(utf16.Encode([]rune(chunk))) > 2000 {
				t.Fatal("oversized chunk")
			}
			if strings.HasPrefix(input, "```") && strings.Count(chunk, "```")%2 != 0 {
				t.Fatal("unclosed fence")
			}
		}
	}
}
