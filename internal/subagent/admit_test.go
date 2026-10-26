package subagent

import (
	"context"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/settings"
)

// With one slot taken, a room root, a service session and a leaf session start
// at once, and a second ordinary turn would wait. The context is cancelled, so
// a turn that waits fails the test at once instead of sleeping.
func TestMachineAdmitHoldsOnlyOrdinaryTurns(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	admit := MachineAdmit(func() settings.Values {
		v := settings.Defaults()
		v.AdmissionMaxTurns = "1"
		return v
	})
	ordinary := Meta{ID: "ses_a", RoomID: "room-a"}
	release, err := admit(t.Context(), ordinary)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	for name, meta := range map[string]Meta{
		"root":    {ID: "ses_r", RoomID: "room-b", Role: RoleRoot},
		"service": {ID: "ses_s", RoomID: "room-b", Service: true},
		"leaf":    {ID: "ses_l", RoomID: "room-b", Leaf: true},
	} {
		if r, err := admit(cancelled, meta); err != nil {
			t.Errorf("%s turn was held: %v", name, err)
		} else {
			r()
		}
	}
	if _, err := admit(cancelled, Meta{ID: "ses_b", RoomID: "room-b"}); err == nil {
		t.Error("a second ordinary turn passed a full pool")
	}
}

// A turn in a paused room holds no machine slot: it gives the slot back when
// the room holds it and takes one again when the room resumes.
func TestRoomHoldGivesTheMachineSlotBack(t *testing.T) {
	e := roomControlEnv(t)
	a, _ := e.start("RESPOND: hi")
	a, _ = e.wait(a.ID, 1)
	e.m.Store.UpdateTurn(a.ID, 1, func(t *Turn) { t.Status = TurnRunning; t.Ended = time.Time{} })
	var held []string
	e.m.Admit = func(context.Context, Meta) (func(), error) {
		held = append(held, "take")
		return func() { held = append(held, "free") }, nil
	}
	slot := &turnSlot{m: e.m, meta: Meta{ID: a.ID, RoomID: a.RoomID}}
	if err := slot.take(e.ctx); err != nil {
		t.Fatal(err)
	}
	writes := e.m.runnerWrites(a.ID, 1)
	writes.suspend, writes.resume = slot.drop, slot.take
	if _, err := e.m.PauseRoomSessions(e.ctx, a.RoomID); err != nil {
		t.Fatal(err)
	}
	e.m.TransferSleep = func(ctx context.Context, _ time.Duration) error {
		if got := strings.Join(held, ","); got != "take,free" {
			t.Errorf("slot events while the room is held = %s, want take,free", got)
		}
		_, err := e.m.ResumeRoomSessions(ctx, a.RoomID)
		return err
	}
	if err := writes.waitSession(e.ctx, "prompt"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(held, ","); got != "take,free,take" {
		t.Fatalf("slot events = %s, want take,free,take", got)
	}
}
