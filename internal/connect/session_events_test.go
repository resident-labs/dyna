package connect

import "testing"

func TestSessionRevocationNotifiesHost(t *testing.T) {
	h := newHarness(t)
	if err := h.store.UpdateSessions(func([]Session) []Session {
		return []Session{{ID: "cs_test", Device: "dev_test", End: h.now.Add(GrantLife)}}
	}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	h.svc.o.SessionsEnded = func() {
		calls++
		list, err := h.store.Sessions()
		if err != nil || len(list) != 0 {
			t.Fatalf("host saw sessions before revocation: %+v, %v", list, err)
		}
	}
	h.svc.endSessions(func(Session) bool { return true }, "test")
	h.svc.endSessions(func(Session) bool { return true }, "already ended")
	if calls != 1 {
		t.Fatalf("revocation notifications = %d", calls)
	}
}
