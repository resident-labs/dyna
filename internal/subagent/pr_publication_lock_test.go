package subagent

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/sessionlife"
)

func TestPRResponsibilitiesReusePublicationLock(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	root := state.Root(filepath.Dir(e.m.Store.Root))
	if err := state.Write(root, state.State{Version: 1, Records: map[string]*state.Record{
		"pr": {Key: "pr", RoomID: info.RoomID, Author: info.ID, Watching: true, State: "open"},
	}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(e.ctx, time.Second)
	defer cancel()
	type result struct {
		items []owned
		err   error
	}
	done := make(chan result, 1)
	joined := false
	var got result
	err := sessionlife.WithPublication(ctx, root, "state.lock", func() error {
		go func() {
			items, err := e.m.prOwned(map[string]Info{info.ID: info})
			done <- result{items, err}
		}()
		select {
		case got = <-done:
			joined = true
			return got.err
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	// On a regression the callback releases the publication lock at its deadline,
	// then joins the blocked reader before the temporary store is removed.
	if !joined {
		got = <-done
	}
	if err != nil {
		t.Fatalf("PR responsibility reader reacquired its publication lock: %v", err)
	}
	if got.err != nil || len(got.items) != 1 || got.items[0].r.Kind != "pr" {
		t.Fatalf("PR responsibilities: %+v, %v", got.items, got.err)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("responsibility check used its deadline")
	}
}
