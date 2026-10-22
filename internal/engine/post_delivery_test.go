package engine

import (
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/websvc"
)

// A post through the engine's Post call is stored before its deliveries run.
// When one delivery fails, the call keeps the stored post as its result, so a
// CLI or a client reports the failure instead of sending the post again.
func TestPostCallKeepsAStoredPostWhenDeliveryFails(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	rooms := roomsvc.New(nil)
	rec, err := rooms.Notify.Rooms.Create(room.CreateInput{Name: "post-delivery"})
	if err != nil {
		t.Fatal(err)
	}
	// A real run is stamped with its room, and its worker mention resolves
	// through that stamp.
	t.Setenv("DYNA_ROOM_ID", rec.ID)
	t.Setenv("DYNA_ROOM", rec.Name)
	run, err := runstore.Create("ended", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	// A run that has ended cannot take steering, so this delivery fails.
	run.Finish("ok", "null", nil)
	t.Setenv("DYNA_ROOM_ID", "")
	t.Setenv("DYNA_ROOM", "")

	ctx := localCallerContext(t, websvc.Caller{Kind: websvc.Owner}, authority.Actor{Role: authority.Owner})
	res, err := services{rooms: rooms}.post(ctx, notify.PostRequest{
		Text:     "read the log",
		Mentions: []string{"@run:" + run.Meta.ID + "/agent:1"},
		Claim:    notify.Claim{Room: rec.Name},
	})
	if err != nil {
		t.Fatalf("a stored post with a failed delivery returned an error: %v", err)
	}
	if res.Message.ID == "" || len(res.Failed()) != 1 {
		t.Fatalf("stored post = %q, failed deliveries = %+v", res.Message.ID, res.Failed())
	}
}
