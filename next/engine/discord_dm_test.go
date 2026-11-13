package engine

import (
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord"
	"testing"
)

func TestDiscordRoutedDMAccess(t *testing.T) {
	f := discordFixture(t)
	caller := boxCaller(f.root)
	for _, id := range []string{"500", "501"} {
		if p := f.s.discordURLScope(f.ctx, caller, "GET", "/api/v10/channels/"+id); p == nil {
			t.Fatal("unrouted DM accessible")
		}
	}
	m := discord.Message{ID: "400", Channel: "500"}
	m.Author.ID = "2"
	if err := f.s.discordInput(f.ctx, f.client, m); err != nil {
		t.Fatal(err)
	}
	if _, p := f.s.DiscordSend(f.ctx, caller, contract.DiscordSendRequest{Channel: "500", Text: "answer", ReplyTo: "400"}); p != nil {
		t.Fatal(p)
	}
	f.fake.SetHistory([]any{})
	if _, p := f.s.DiscordRead(f.ctx, caller, contract.DiscordReadRequest{Channel: "500"}); p != nil {
		t.Fatal(p)
	}
	if _, p := f.s.DiscordRead(f.ctx, caller, contract.DiscordReadRequest{Channel: "501"}); p == nil {
		t.Fatal("read unrouted DM")
	}
	scope, p := f.s.discordReadScope(f.ctx, caller)
	if p != nil {
		t.Fatal(p)
	}
	// Persisted routing, not the in-memory directory, resolves the DM.
	c, ok := scope.channel(&discord.Directory{}, "500")
	if !ok || c.Guild != "100" || !scope.allowsChannel(c) {
		t.Fatal(c, ok)
	}
	scope.room = "another-room"
	if scope.allowsChannel(c) {
		t.Fatal("another room can access DM")
	}
	for _, method := range []string{"DELETE", "PATCH"} {
		if p := f.s.discordCap(f.ctx, caller, method, "/api/v10/channels/500", nil); p == nil {
			t.Fatal("DM moderation allowed")
		}
	}
	if err := f.s.store.SetDiscordRoute(f.ctx, scope.routes[0], true); err != nil {
		t.Fatal(err)
	}
	if p := f.s.discordURLScope(f.ctx, caller, "GET", "/api/v10/channels/500"); p == nil {
		t.Fatal("removed mapping retains DM access")
	}
}
