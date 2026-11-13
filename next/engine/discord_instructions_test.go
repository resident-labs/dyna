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
	fixture := discordFixture(t)
	front := startChild(t, fixture.Service, fixture.Context, fixture.Room, "front", fixture.Root)
	if err := fixture.Service.store.SetDiscordRoute(fixture.Context, store.DiscordRoute{Guild: "100", Room: fixture.Room, Session: front, Instructions: "old"}, false); err != nil {
		t.Fatal(err)
	}
	text := "Pin on request. Never ban."
	if _, p := fixture.Service.DiscordInstructions(fixture.Context, boxCaller(front), contract.DiscordInstructionsRequest{Session: "front", Text: &text}); p == nil || p.Code != contract.ErrForbidden {
		t.Fatalf("the front set its own instructions: %v", p)
	}
	if got, p := fixture.Service.DiscordInstructions(fixture.Context, boxCaller(fixture.Root), contract.DiscordInstructionsRequest{Session: "front"}); p != nil || got.Text != "old" {
		t.Fatalf("read %+v %v", got, p)
	}
	if got, p := fixture.Service.DiscordInstructions(fixture.Context, boxCaller(fixture.Root), contract.DiscordInstructionsRequest{Session: "front", Text: &text}); p != nil || got.Text != text {
		t.Fatalf("set %+v %v", got, p)
	}
	long := strings.Repeat("x", 4001)
	for name, req := range map[string]contract.DiscordInstructionsRequest{"too long": {Session: "front", Text: &long}, "unknown session": {Session: "nobody", Text: &text}, "answers nothing": {Session: "root", Text: &text}} {
		if _, p := fixture.Service.DiscordInstructions(fixture.Context, boxCaller(fixture.Root), req); p == nil || p.Code != contract.ErrInvalid {
			t.Errorf("%s: %v", name, p)
		}
	}
	routes, err := fixture.Service.store.DiscordRoutes(fixture.Context, fixture.Room)
	if err != nil || len(routes) != 1 || routes[0].Session != front || routes[0].Guild != "100" || routes[0].Instructions != text {
		t.Fatalf("routes %+v %v", routes, err)
	}
	snapshot, p := fixture.Service.ContextSnapshot(fixture.Context, boxCaller(front), contract.ContextSnapshotRequest{Section: contract.ContextDiscord})
	if p != nil || len(snapshot.Sections) != 1 || !strings.Contains(snapshot.Sections[0].Text, "Your standing instructions, from the owner or root:\n"+text) {
		t.Fatalf("front context %+v %v", snapshot, p)
	}
}
