package goals

import (
	"testing"
	"time"
)

func TestExpediteOnePerRoom(t *testing.T) {
	s := lifecycleStore(t)
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	a, _ := s.Add("r", "owner", Input{Text: "a", Shared: true})
	b, _ := s.Add("r", "owner", Input{Text: "b", Shared: true})
	other, _ := s.Add("other", "owner", Input{Text: "c", Shared: true})

	if _, err := s.Expedite("r", a.ID, false); err != nil {
		t.Fatal(err)
	}
	first := now
	now = now.Add(time.Hour)
	if g, err := s.Expedite("r", a.ID, false); err != nil || !g.ExpediteUntil.Equal(first.Add(ExpediteTTL)) {
		t.Fatalf("same goal must be idempotent: %v %v", g.ExpediteUntil, err)
	}
	if _, err := s.Expedite("r", b.ID, false); err == nil {
		t.Fatal("a second goal needs replace")
	}
	if _, err := s.Expedite("other", other.ID, false); err != nil {
		t.Fatalf("rooms are independent: %v", err)
	}
	if _, err := s.Expedite("r", b.ID, true); err != nil {
		t.Fatal(err)
	}
	if s.IsExpedited("r", a.ID) || !s.IsExpedited("r", b.ID) {
		t.Fatal("replace must move the priority")
	}

	// Closing clears it.
	if _, err := s.Finish("r", b.ID, "owner", "done", false); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.Expedited("r"); g != nil {
		t.Fatalf("finished goal stays expedited: %+v", g)
	}
	if got, _ := s.Get("r", b.ID); got.ExpediteUntil != nil {
		t.Fatal("finish must clear the stored time")
	}

	// Expiry shows on every read path.
	if _, err := s.Expedite("r", a.ID, false); err != nil {
		t.Fatal(err)
	}
	now = now.Add(ExpediteTTL + time.Second)
	if g, _ := s.Expedited("r"); g != nil {
		t.Fatal("expired priority must not read as expedited")
	}
	if got, _ := s.Get("r", a.ID); got.ExpediteUntil != nil {
		t.Fatal("expired time must not be returned")
	}
	if st, _ := s.Read(); st.Rooms["r"].Goals[a.ID].ExpediteUntil != nil {
		t.Fatal("Read must drop an expired time")
	}
	// An expired mark does not block another goal.
	if _, err := s.Expedite("r", b.ID, false); err == nil {
		t.Fatal("closed goal cannot be expedited")
	}
	c, _ := s.Add("r", "owner", Input{Text: "c", Shared: true})
	if _, err := s.Expedite("r", c.ID, false); err != nil {
		t.Fatalf("expired mark blocked a new goal: %v", err)
	}
	if g, err := s.StopExpedite("r"); err != nil || g.ID != c.ID || g.ExpediteUntil != nil {
		t.Fatalf("stop: %+v %v", g, err)
	}
	if g, _ := s.Expedited("r"); g != nil {
		t.Fatal("stop must clear")
	}
}
