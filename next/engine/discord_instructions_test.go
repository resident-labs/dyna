package engine

import (
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// Root sets the standing instructions of the session that answers Discord for it; no other session may, and the
// answering session reads them in its context.
func TestRootSetsTheAnsweringSessionsInstructions(t *testing.T) {
	f := discordFixture(t)
	front := startChild(t, f.s, f.ctx, f.room, "front", f.root)
	if err := f.s.store.SetDiscordRoute(f.ctx, store.DiscordRoute{Guild: "100", Room: f.room, Session: front, Instructions: "old"}, false); err != nil {
		t.Fatal(err)
	}
	text := "Pin on request. Never ban."
	if _, p := f.s.DiscordInstructions(f.ctx, boxCaller(front), contract.DiscordInstructionsRequest{Session: "front", Text: &text}); p == nil || p.Code != contract.ErrForbidden {
		t.Fatalf("the front set its own instructions: %v", p)
	}
	if got, p := f.s.DiscordInstructions(f.ctx, boxCaller(f.root), contract.DiscordInstructionsRequest{Session: "front"}); p != nil || got.Text != "old" {
		t.Fatalf("read %+v %v", got, p)
	}
	if got, p := f.s.DiscordInstructions(f.ctx, boxCaller(f.root), contract.DiscordInstructionsRequest{Session: "front", Text: &text}); p != nil || got.Text != text {
		t.Fatalf("set %+v %v", got, p)
	}
	long := strings.Repeat("x", 4001)
	for name, req := range map[string]contract.DiscordInstructionsRequest{"too long": {Session: "front", Text: &long}, "unknown session": {Session: "nobody", Text: &text}, "answers nothing": {Session: "root", Text: &text}} {
		if _, p := f.s.DiscordInstructions(f.ctx, boxCaller(f.root), req); p == nil || p.Code != contract.ErrInvalid {
			t.Errorf("%s: %v", name, p)
		}
	}
	routes, err := f.s.store.DiscordRoutes(f.ctx, f.room)
	if err != nil || len(routes) != 1 || routes[0].Session != front || routes[0].Guild != "100" || routes[0].Instructions != text {
		t.Fatalf("routes %+v %v", routes, err)
	}
	snapshot, p := f.s.ContextSnapshot(f.ctx, boxCaller(front), contract.ContextSnapshotRequest{Section: contract.ContextDiscord})
	if p != nil || len(snapshot.Sections) != 1 || !strings.Contains(snapshot.Sections[0].Text, "Your standing instructions, from the owner or root:\n"+text) {
		t.Fatalf("front context %+v %v", snapshot, p)
	}
}
