package runtime

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// The independent reproduction's exact native StopContainer barrier: the real
// wake restores run and a legal ready transition/first turn precedes host stop.
func TestStopWakeReadyPreservesNamedWorkAtNativeBoundary(t *testing.T) {
	f := newRestartFixture(t)
	ctx := context.Background()
	f.exec(t, `DELETE FROM turns; UPDATE boxes SET state='starting',want='stop',image='first-image',due_at=0 WHERE id='b'`)
	if err := f.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := f.st.AppendEvent(tx, contract.Event{ID: "stop-wake-initial", Room: "r", Time: f.now.Add(-time.Minute), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{Box: "b", Session: "s", To: contract.BoxPending}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RESTART_GATE", "yes")
	if err := f.svc.Reconciler().Pass(ctx); err != nil {
		t.Fatal(err)
	}
	waitPinnedFile(t, f.entered)
	if err := f.st.WakeWithNotice(ctx, contract.Event{ID: "stop-wake-input", Room: "r", Time: f.now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionNotice{Session: "s", Text: "new input during Stop"}}, "s"); err != nil {
		t.Fatal(err)
	}
	old, err := f.st.Box(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.TransitionBox(ctx, old, contract.BoxReady, "", contract.Event{ID: "stop-wake-ready", Room: "r", Time: f.now.Add(time.Second), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{}}); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO turns(id,session,started_at,ended_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('stop-wake-used','s',3,4,'completed','p',0,0,0,0)`)
	for _, dir := range []string{f.workspace, f.stack} {
		if err := os.WriteFile(filepath.Join(dir, "new-used-work"), []byte("written after wake/ready/turn"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(f.release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	f.svc.Reconciler().Wait()
	for _, dir := range []string{f.workspace, f.stack} {
		assertRestartFile(t, dir, "new-used-work", true)
	}
}
