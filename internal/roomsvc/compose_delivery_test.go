package roomsvc

import (
	"testing"

	"dyna-agent/internal/runstore"
)

// A post that is stored has been sent. When one delivery fails, the composer
// must get the stored post and its failures, not an error that makes it offer
// the same text again for a duplicate post.
func TestStoredPostKeepsItsMessageWhenDeliveryFails(t *testing.T) {
	e := newEnv(t)
	rec := e.room("delivery")
	// A real run is stamped with its room, and the worker mention resolves
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
	res, err := e.svc.Post(e.ctx, rec.ID, "@run:"+run.Meta.ID+"/agent:1 read the log", "")
	if err != nil {
		t.Fatalf("a stored post with a failed delivery returned an error: %v", err)
	}
	if res.Message.ID == "" || len(res.Failed) != 1 {
		t.Fatalf("stored post = %q, failed deliveries = %+v", res.Message.ID, res.Failed)
	}
}
