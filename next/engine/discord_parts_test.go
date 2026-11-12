package engine

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord"
	"github.com/resident-labs/dyna/room"
)

// A mention about an image reaches the session with the image when it replies to it, and with the image's name and
// message ID when it only follows it. Embeds, stickers and forwarded messages show as a line, in the input and in a read.
func TestDiscordMentionSeesWhatItIsAbout(t *testing.T) {
	s, r, root, ctx, fake, client := discordFixture(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	old := http.DefaultTransport
	http.DefaultTransport = discordFileTransport{old}
	t.Cleanup(func() { http.DefaultTransport = old })
	image := map[string]any{"filename": "affair.png", "size": 12, "url": "https://cdn.discordapp.com/image"}
	ari := map[string]any{"id": "2", "username": "ari"}
	fake.SetHistory([]any{
		map[string]any{"id": "29", "timestamp": "2026-11-01T12:03:00Z", "content": "", "author": ari, "embeds": []any{map[string]any{"url": "https://tenor.com/view/x"}}},
		map[string]any{"id": "28", "timestamp": "2026-11-01T12:02:00Z", "content": "", "author": ari, "sticker_items": []any{map[string]any{"name": "wave"}}},
		map[string]any{"id": "27", "timestamp": "2026-11-01T12:01:00Z", "content": "", "author": ari, "message_snapshots": []any{map[string]any{"message": map[string]any{"content": "from elsewhere", "attachments": []any{image}}}}},
		map[string]any{"id": "26", "timestamp": "2026-11-01T12:00:00Z", "content": "third affair?", "author": ari, "attachments": []any{image}},
	})
	raw, err := json.Marshal(map[string]any{"id": "300", "channel_id": "200", "guild_id": "100", "content": "what do you think", "author": ari,
		"referenced_message": map[string]any{"id": "26", "content": "third affair?", "author": ari, "attachments": []any{image}}})
	if err != nil {
		t.Fatal(err)
	}
	var m discord.Message
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if err := s.discordInput(ctx, client, m); err != nil {
		t.Fatal(err)
	}
	events, err := s.store.Events(ctx, r, 0, 100, []contract.EventType{"discord.message"})
	if err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
	body := events[0].Body.(contract.DiscordMessage)
	if len(body.Files) != 1 || body.Files[0].Name != "affair.png" {
		t.Errorf("the replied-to image was not delivered: %+v", body.Attachments)
	}
	rendered, err := room.RenderDelivery(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ari: third affair? [files: affair.png; discord read with message 26 delivers them]",
		"ari:  [Forwarded: from elsewhere] [files: affair.png; discord read with message 27 delivers them]",
		"ari:  [Sticker: wave]", "ari:  [Embed: https://tenor.com/view/x]",
		"Reply to @ari (message 26): third affair?",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("input lacks %q:\n%s", want, rendered)
		}
	}
	got, p := s.DiscordRead(ctx, boxCaller(root), contract.DiscordReadRequest{Channel: "200"})
	if p != nil {
		t.Fatal(p)
	}
	for _, want := range []string{"message 27\n\nForwarded: from elsewhere\nAttachment 1: affair.png (12 bytes)", "message 28\n\nSticker: wave", "message 29\n\nEmbed: https://tenor.com/view/x"} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("read lacks %q:\n%s", want, got.Text)
		}
	}
}
