package lifecycle

import (
	"database/sql"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestStartupFailsStaleCreatingBox(t *testing.T) {
	rig := newHostRig(t)
	setAge := func(age time.Duration) {
		t.Helper()
		err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec("UPDATE boxes SET state='creating', want='run', updated_at=? WHERE id='b'", rig.now.Add(-age).UnixMilli())
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	setAge(15 * time.Minute)
	if err := rig.r.FailStaleCreates(rig.ctx); err != nil {
		t.Fatal(err)
	}
	b, err := rig.st.Box(rig.ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	if b.State != contract.BoxCreating {
		t.Fatalf("fresh box: %s", b.State)
	}
	setAge(16 * time.Minute)
	if err := rig.r.FailStaleCreates(rig.ctx); err != nil {
		t.Fatal(err)
	}
	b, err = rig.st.Box(rig.ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	if b.State != contract.BoxFailed || b.Reason != string(contract.CauseCreateFailed) {
		t.Fatalf("stale box: %s %q", b.State, b.Reason)
	}
	leftovers, err := rig.st.Leftovers(rig.ctx, b.Host)
	if err != nil || len(leftovers) != 1 || leftovers[0].Box != "b" {
		t.Fatalf("leftovers %+v: %v", leftovers, err)
	}
}
