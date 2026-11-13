package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord"
)

// One bot speaks for several sessions, so the answering one sees what the bot said, cut short, and the server's channels.
func TestDiscordAnsweringSessionSeesTheBotAndTheChannels(t *testing.T) {
	fixture := discordFixture(t)
	fixture.Fake.SetHistory([]any{
		map[string]any{"id": "29", "content": "pin it in music", "author": map[string]string{"id": "2", "username": "ari"}},
		map[string]any{"id": "28", "content": "Made #music. " + strings.Repeat("x", 400), "author": map[string]string{"id": "900", "username": "Kira"}},
	})
	var m discord.Message
	if err := json.Unmarshal([]byte(`{"id":"300","channel_id":"200","guild_id":"100","content":"question","author":{"id":"2","username":"ari"}}`), &m); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Service.discordInput(fixture.Context, fixture.Client, m); err != nil {
		t.Fatal(err)
	}
	events, err := fixture.Service.store.Events(fixture.Context, fixture.Room, 0, 100, []contract.EventType{"discord.message"})
	if err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
	history := events[0].Body.(contract.DiscordMessage).History
	if !strings.HasPrefix(history, "Kira: Made #music. xxx") || !strings.HasSuffix(history, "x…\nari: pin it in music") || len([]rune(history)) > 330 {
		t.Errorf("history %q", history)
	}
	fixture.Service.discordDirectory.Apply("CHANNEL_CREATE", json.RawMessage(`{"id":"777","guild_id":"100","name":"music","type":0}`))
	fixture.Service.discordDirectory.Apply("CHANNEL_CREATE", json.RawMessage(`{"id":"778","guild_id":"100","name":"Voice","type":2}`))
	snapshot, problem := fixture.Service.ContextSnapshot(fixture.Context, boxCaller(fixture.Root), contract.ContextSnapshotRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	var section string
	for _, sec := range snapshot.Sections {
		if sec.Key == contract.ContextDiscord {
			section = sec.Text
		}
	}
	if !strings.Contains(section, "#music (777)") || strings.Contains(section, "Voice") {
		t.Errorf("section %q", section)
	}
}
