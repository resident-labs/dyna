package subagent

import (
	"context"
	"testing"
	"time"

	"dyna-agent/internal/shells"
)

func TestCloseStopsOwnedShells(t *testing.T) {
	e := newTestEnv(t)
	session, turn := e.start("SLEEP:1 RESPOND: shell owner")
	e.wait(session.ID, turn.N)
	store := shells.DefaultStore()
	var metas []shells.Meta
	for _, keep := range []bool{false, true} {
		meta, err := store.Create(shells.StartOptions{Owner: session.ID, Keep: keep, Cmd: []string{"sleep", "600"}, Cwd: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- store.Supervise(ctx, meta.ID) }()
		t.Cleanup(func() {
			cancel()
			if err := <-done; err != nil {
				t.Errorf("supervisor: %v", err)
			}
		})
		started := false
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
			in, err := store.Info(meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			if in.Status == "running" && in.Pid > 0 {
				started = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !started {
			t.Fatal("shell did not start")
		}
		metas = append(metas, meta)
	}
	if _, err := e.m.Close(e.ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	for _, meta := range metas {
		in, err := store.Info(meta.ID)
		want := "exited"
		if meta.Keep {
			want = "running"
		}
		if err != nil || in.Status != want {
			t.Fatalf("shell keep=%v: status=%s, want %s, error=%v", meta.Keep, in.Status, want, err)
		}
	}
}
