package lifecycle

import (
	"database/sql"
	"testing"
)

// The relocation procedure removes only an already stopped container, with the engine down.
// On resume, absence (even with an unchanged image) must take Recreate, not Start or fresh Create.
func TestRelocatedStoppedBoxRecreatesAfterRoomResume(t *testing.T) {
	rig := newHostRig(t)
	sentinel := 7
	called := &sentinel
	rig.r.boxes = recreatedBoxes{restartedBoxes: restartedBoxes{image: "image"}, card: &called}
	rig.stop(false, "")
	if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error { _, err := tx.Exec("UPDATE rooms SET paused_at=1 WHERE id='r'"); return err }); err != nil {
		t.Fatal(err)
	}
	rig.pass()
	if called == nil {
		t.Fatal("paused room recreated its container")
	}
	if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error { _, err := tx.Exec("UPDATE rooms SET paused_at=NULL WHERE id='r'"); return err }); err != nil {
		t.Fatal(err)
	}
	rig.pass()
	if called != nil {
		t.Fatal("missing stopped container did not use Recreate on resume")
	}
}
