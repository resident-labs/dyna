package engine

import (
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// A session answering Discord is told to acknowledge first, except one whose mapping carries the owner's instructions.
func TestDiscordFrameAsksForQuickAcknowledgement(t *testing.T) {
	f := discordFixture(t)
	event := contract.Event{Room: f.room, Body: contract.DiscordMessage{Channel: "200", Message: "300", ChannelName: "general", GuildName: "server"}}
	owed := contract.Delivery{Reply: contract.ReplyChannel{Kind: contract.ReplyFinal}}
	const ask = "A person is waiting in that channel."
	frame, _, err := f.s.inputFrame(f.ctx, f.root, event, owed)
	if err != nil || strings.Contains(frame, ask) {
		t.Fatalf("a mapping with instructions got the ask: %s %v", frame, err)
	}
	if err := f.s.store.SetDiscordRoute(f.ctx, store.DiscordRoute{Guild: "100", Room: f.room, Session: f.root}, false); err != nil {
		t.Fatal(err)
	}
	frame, _, err = f.s.inputFrame(f.ctx, f.root, event, owed)
	if err != nil || !strings.Contains(frame, ask) || !strings.Contains(frame, "no replyTo") {
		t.Fatalf("a mapping without instructions lacks the ask: %s %v", frame, err)
	}
	frame, _, err = f.s.inputFrame(f.ctx, f.root, event, contract.Delivery{Reply: contract.ReplyChannel{Kind: contract.ReplyNone}})
	if err != nil || strings.Contains(frame, ask) {
		t.Fatalf("an input that owes no reply got the ask: %s %v", frame, err)
	}
}
