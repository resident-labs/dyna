package lifecycle

import (
	"context"
	"database/sql"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// missingMount refuses its create and its start for a source the box mounts that is not there.
type missingMount struct {
	runningBoxes
	creates, starts int
}

const missingMountText = "agy account folder missing at /host/agy/accounts/main: sign in first"

func (m *missingMount) Create(context.Context, BoxSpec) (string, error) {
	m.creates++
	return "", box.MissingMount{Text: missingMountText}
}
func (m *missingMount) Start(context.Context, contract.BoxID) error {
	m.starts++
	return box.MissingMount{Text: missingMountText}
}
func (*missingMount) CurrentImage(context.Context, contract.SessionID, contract.HostName) (box.Image, error) {
	return box.Image{Tag: "image"}, nil
}

// A missing source fails the box at once with its text kept, with no retry, whichever way the box starts.
func TestMissingMountFailsTheBoxWithItsText(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		cause       contract.FailureCause
	}{{"create", "creating", contract.CauseCreateFailed}, {"warm start", "stopped", contract.CauseStartFailed}} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newHostRig(t)
			boxes := &missingMount{}
			rig.r.boxes = boxes
			rig.view.set("s", false)
			if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec("UPDATE boxes SET state=?,reason='',due_at=0,failures=0,image=CASE WHEN ?='creating' THEN '' ELSE 'image' END", tc.state, tc.state)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			got := rig.pass()
			if got.State != contract.BoxFailed || got.Reason != missingMountText {
				t.Fatalf("box after the refusal: %s %q", got.State, got.Reason)
			}
			var cause string
			if err := rig.st.Read().QueryRow(`SELECT json_extract(body,'$.cause') FROM events WHERE type='box.changed' ORDER BY seq DESC LIMIT 1`).Scan(&cause); err != nil || cause != string(tc.cause) {
				t.Fatalf("cause %q: %v", cause, err)
			}
			if boxes.creates+boxes.starts != 1 {
				t.Fatalf("attempts: %d creates, %d starts", boxes.creates, boxes.starts)
			}
			rig.pass()
			if boxes.creates+boxes.starts != 1 {
				t.Fatalf("the failed box was tried again: %d creates, %d starts", boxes.creates, boxes.starts)
			}
		})
	}
}
