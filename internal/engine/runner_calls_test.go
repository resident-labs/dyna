package engine

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/database"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/workeroutput"
)

// A runner resends its finish call with the same request ID when the reply
// is lost. The engine answers the resend without running it, so the turn's
// usage is added to the session once. A new request ID adds it again.
func TestRunnerFinishResentAddsUsageOnce(t *testing.T) {
	dataDir := storetest.DataDir(t)
	if err := state.MigrateRequests(dataDir); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dataDir) })
	sessions := filepath.Join(dataDir, "sessions")
	const id = "ses_20261020-120000-00000001"
	recordtest.Write(t, sessions, id, subagent.Meta{ID: id, Name: "runner", RoomID: "room_20261020-120000-00000001", Room: "work", Turns: 1})
	m := &subagent.Manager{Store: &subagent.Store{Root: sessions}}
	once := &requestRunner{w: state.For(db), logf: t.Logf}
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: id})
	finish := func(ctx context.Context, _ []json.RawMessage) (any, error) {
		return nil, m.RunnerFinish(ctx, subagent.RunnerFinish{Turn: 1, Usage: workeroutput.Usage{InputTokens: 10}})
	}
	usage := func() int64 {
		t.Helper()
		meta, err := m.Store.ReadMeta(id)
		if err != nil {
			t.Fatal(err)
		}
		return meta.Usage.InputTokens
	}
	for range 2 {
		if _, err := once.run(ctx, "runner/1", "RunnerFinish", finish, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := usage(); got != 10 {
		t.Fatalf("usage after a resent finish = %d; want 10, added once", got)
	}
	if _, err := once.run(ctx, "runner/2", "RunnerFinish", finish, nil); err != nil {
		t.Fatal(err)
	}
	if got := usage(); got != 20 {
		t.Fatalf("usage after a new finish = %d; want 20", got)
	}
}

// The engine stops after RunnerFinish commits but before it stores the
// call's result. The runner then resends the call with the same request ID.
// The ID was recorded in the same transaction as the usage, so the resend
// writes nothing and the usage is still counted once.
func TestRunnerFinishResentAfterACrashAddsUsageOnce(t *testing.T) {
	dataDir := storetest.DataDir(t)
	if err := state.MigrateRequests(dataDir); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dataDir) })
	sessions := filepath.Join(dataDir, "sessions")
	const id = "ses_20261020-120000-00000002"
	recordtest.Write(t, sessions, id, subagent.Meta{ID: id, Name: "runner", RoomID: "room_20261020-120000-00000001", Room: "work", Turns: 1})
	m := &subagent.Manager{Store: &subagent.Store{Root: sessions}}
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: id})
	f := subagent.RunnerFinish{Turn: 1, Usage: workeroutput.Usage{InputTokens: 10}}
	// The first attempt commits, then the engine stops: no stored result.
	if err := m.RunnerFinish(state.ForRequest(ctx, "runner/1"), f); err != nil {
		t.Fatal(err)
	}
	once := &requestRunner{w: state.For(db), logf: t.Logf}
	resend := func(ctx context.Context, _ []json.RawMessage) (any, error) { return nil, m.RunnerFinish(ctx, f) }
	if _, err := once.run(ctx, "runner/1", "RunnerFinish", resend, nil); err != nil {
		t.Fatal(err)
	}
	meta, err := m.Store.ReadMeta(id)
	if err != nil || meta.Usage.InputTokens != 10 {
		t.Fatalf("usage after a resend past a crash = %d, %v; want 10, added once", meta.Usage.InputTokens, err)
	}
}
