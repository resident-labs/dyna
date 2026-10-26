package subagent

import (
	"context"
	"testing"

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
