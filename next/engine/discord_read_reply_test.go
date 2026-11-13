package engine

import (
	"github.com/resident-labs/dyna/contract"
	"strings"
	"testing"
)

func TestDiscordReadCompactReply(t *testing.T) {
	f := discordFixture(t)
	f.fake.SetHistory([]any{
		map[string]any{"id": "301", "message_reference": map[string]any{"message_id": "299"}, "referenced_message": map[string]any{"id": "299", "content": "first\nsecond", "author": map[string]any{"username": "ari"}}},
		map[string]any{"id": "302", "message_reference": map[string]any{"message_id": "298"}},
	})
	got, p := f.s.DiscordRead(f.ctx, boxCaller(f.root), contract.DiscordReadRequest{Channel: "general"})
	if p != nil {
		t.Fatal(p)
	}
	if strings.Contains(got.Text, "Reply to 299") || !strings.Contains(got.Text, "Reply: @ari (message 299): first second\n") || !strings.Contains(got.Text, "Reply to 298") {
		t.Fatal(got.Text)
	}
}
