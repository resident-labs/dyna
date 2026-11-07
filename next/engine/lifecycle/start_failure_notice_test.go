package lifecycle

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/room"
)

type repeatedStartFailure struct {
	runningBoxes
	calls     int
	recovered bool
}

func (*repeatedStartFailure) CurrentImage(context.Context, contract.SessionID, contract.HostName) (box.Image, error) {
	return box.Image{Tag: "image"}, nil
}
func (b *repeatedStartFailure) Start(context.Context, contract.BoxID) error {
	b.calls++
	if b.recovered {
		return nil
	}
	return errors.New("podman start: container is being removed")
}

func TestRepeatedStartFailureNotice(t *testing.T) {
	rig := newHostRig(t)
	runtime := &repeatedStartFailure{}
	rig.r.boxes = runtime
	rig.view.set("s", false)
	if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE boxes SET state='stopped',reason='',due_at=0")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 4; attempt++ {
		got := rig.pass()
		rows, err := rig.st.LifecycleBoxes(rig.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got.State != contract.BoxStopped || rows[0].Failures != attempt || !rows[0].DueAt.After(rig.now) {
			t.Fatalf("attempt %d lost retry semantics: %+v", attempt, rows[0])
		}
		if attempt == 1 && got.Reason != "" {
			t.Fatalf("first attempt changed reason: %q", got.Reason)
		}
		if attempt == 2 && got.Reason != "podman start: container is being removed" {
			t.Fatalf("second attempt lost actual error: %q", got.Reason)
		}
		var failures, notices int
		if err := rig.st.Read().QueryRow(`SELECT count(*) FROM events WHERE type='box.changed' AND json_extract(body,'$.cause')='start_failed'`).Scan(&failures); err != nil {
			t.Fatal(err)
		}
		if err := rig.st.Read().QueryRow(`SELECT count(*) FROM deliveries d JOIN events e ON e.seq=d.seq WHERE d.to_id='s' AND e.type='box.changed'`).Scan(&notices); err != nil {
			t.Fatal(err)
		}
		want := 1
		if attempt == 1 {
			want = 0
		}
		if failures != want || notices != want {
			t.Fatalf("attempt %d: failure events=%d agent notices=%d, want %d", attempt, failures, notices, want)
		}
		rig.now = rows[0].DueAt.Add(time.Second)
	}
	var raw []byte
	var wake bool
	if err := rig.st.Read().QueryRow(`SELECT e.body,d.wake FROM deliveries d JOIN events e ON e.seq=d.seq WHERE e.type='box.changed'`).Scan(&raw, &wake); err != nil {
		t.Fatal(err)
	}
	var changed contract.BoxChanged
	if err := json.Unmarshal(raw, &changed); err != nil {
		t.Fatal(err)
	}
	text, err := room.RenderDelivery(changed)
	if err != nil || wake || !strings.Contains(text, "podman start: container is being removed") {
		t.Fatalf("agent notice: %q, wake=%v, error=%v", text, wake, err)
	}
	runtime.recovered = true
	got := rig.pass()
	if got.State != contract.BoxStarting || runtime.calls != 5 {
		t.Fatalf("automatic retry did not recover: %+v calls=%d", got, runtime.calls)
	}
}
