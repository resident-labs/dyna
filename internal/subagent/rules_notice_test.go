package subagent

import (
	"testing"

	"dyna-agent/internal/goals"
)

func TestRuleChangesLeaveNoPendingRoomMessages(t *testing.T) {
	e := newTestEnv(t)
	first := completedFixtureSession(t, e, "RESPOND: ready")
	second := completedFixtureSession(t, e, "RESPOND: ready")
	first, _ = e.wait(first.ID, 1)
	second, _ = e.wait(second.ID, 1)
	rec, err := e.m.rooms().Resolve(first.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	store := goals.DefaultStore()
	rule, err := store.AddRule(rec.ID, "owner", "owner", "Keep tests fast")
	if err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"added", "edited", "removed"} {
		if verb == "edited" {
			rule, err = store.EditRule(rec.ID, rule.ID, "owner", "owner", "Run quick tests")
		}
		if verb == "removed" {
			rule, err = store.RemoveRule(rec.ID, rule.ID, "owner")
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := goals.PostRuleNotice(e.m.rooms(), rec, rule, "owner", verb); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{first.ID, second.ID} {
			meta, err := e.m.Store.ReadMeta(id)
			if err != nil {
				t.Fatal(err)
			}
			pending, err := e.m.pendingRoomMessages(meta)
			if err != nil || len(pending) != 0 {
				t.Fatal(verb, id, pending, err)
			}
			if meta.Turns != 1 {
				t.Fatal("rule change woke session", meta)
			}
		}
	}
}
