package lifecycle

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

type supersededStopBoxes struct {
	runningBoxes
	calls int
}

func (b *supersededStopBoxes) Stop(context.Context, contract.BoxID) error {
	b.calls++
	return box.NamespaceSuperseded{Box: "b"}
}
func (b *supersededStopBoxes) Remove(context.Context, contract.BoxID) error {
	b.calls++
	return box.NamespaceSuperseded{Box: "b"}
}

func TestSupersededStopCompletesWithoutFailureOrRetry(t *testing.T) {
	f := newHostRig(t)
	b := &supersededStopBoxes{}
	f.r.boxes = b
	if err := f.st.Write(f.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE boxes SET state='starting',want='stop',due_at=0,failures=0")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := f.st.LifecycleBoxes(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.r.step(f.ctx, rows[0]); err != nil {
		t.Fatal(err)
	}
	rows, err = f.st.LifecycleBoxes(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].State != contract.BoxStopped || rows[0].Failures != 0 || !rows[0].DueAt.IsZero() {
		t.Fatalf("superseded Stop was not terminal: %+v", rows[0])
	}
	f.now = f.now.Add(time.Hour)
	if err := f.r.Pass(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.r.Wait()
	if b.calls != 1 {
		t.Fatalf("superseded Stop retried: %d calls", b.calls)
	}
}

func TestSupersededCloseCompletesWithoutRetry(t *testing.T) {
	f := newHostRig(t)
	b := &supersededStopBoxes{}
	f.r.boxes = b
	if err := f.st.Write(f.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE boxes SET state='closing',want='close',due_at=0,failures=0")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := f.st.LifecycleBoxes(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.r.closeStep(f.ctx, rows[0]); err != nil {
		t.Fatal(err)
	}
	current, err := f.st.Box(f.ctx, rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != contract.BoxDeleted {
		t.Fatalf("superseded close was not terminal: %+v", current)
	}
	var due int64
	if err := f.st.Read().QueryRow("SELECT due_at FROM boxes WHERE id=?", current.ID).Scan(&due); err != nil || due != 0 {
		t.Fatalf("superseded close scheduled retry: %d %v", due, err)
	}
}
