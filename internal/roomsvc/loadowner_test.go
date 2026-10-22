package roomsvc

import "testing"

func TestLoadOwnerResolvesSessionAndFallsBackToID(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	got := e.svc.LoadOwner(lead.ID)
	if got.Name != "lead" || got.Address != "alpha:lead" || got.Room != "alpha" || got.RoomID != rec.ID || got.Hue != NameHue("alpha:lead") || got.ID != lead.ID {
		t.Fatalf("session owner = %+v", got)
	}
	for _, id := range []string{"ses_unknown", "wf_unknown"} {
		got := e.svc.LoadOwner(id)
		if got.ID != id || got.Name != id || got.Address != "" || got.Room != "" {
			t.Fatalf("owner of %s = %+v, want the ID as the name", id, got)
		}
	}
}
