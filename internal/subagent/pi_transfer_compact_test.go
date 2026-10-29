package subagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/workeroutput"
)

// nativePiTransferStarting starts a Pi session, then moves a native Pi-to-Pi
// transfer into "starting" with its replacement turn held open and no output.
func nativePiTransferStarting(t *testing.T, configure ...func(*profile.Profile)) (*testEnv, *time.Time, Info, TransferState, *heldTurnSpawner, Spawner) {
	t.Helper()
	path, logPath := installFakePiTurn(t)
	old := piTestProfile("pi-old", "old/provider-model", path, logPath)
	next := piTestProfile("pi-next", "new/provider-model", path, logPath)
	for _, fn := range configure {
		fn(&next)
	}
	e := newTestEnv(t, old, next)
	now := time.Now()
	e.m.TransferNow = func() time.Time { return now }
	e.m.TransferLaunch = func(*Manager, string, string) error { return nil }
	info, _ := e.start("RESPOND: first turn", func(so *StartOptions) { so.Profile = "pi-old" })
	info, first := e.wait(info.ID, 1)
	if first.HarnessSession != "" {
		persistFakePiSession(t, first.HarnessSession)
	}
	state, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "pi-next", WaitSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	if state.Phase != "stopping" {
		t.Fatalf("Pi-to-Pi transfer did not start native: %+v", state)
	}
	runner := e.m.Spawner
	held := &heldTurnSpawner{}
	e.m.Spawner = held
	advance(t, e, state, 2)
	if held.lock == nil {
		t.Fatal("replacement turn did not start")
	}
	return e, &now, info, state, held, runner
}

// A Pi history larger than the target window is compacted before the first
// request. That compaction is startup progress: it moves the deadline once,
// so a slow compact is not mistaken for a stalled model.
func TestPiTransferCountsCompactionAsStartupProgress(t *testing.T) {
	e, now, info, state, held, runner := nativePiTransferStarting(t)
	normalized, err := e.m.Store.NormalizedPath(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(normalized), 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(normalized, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"v":1,"kind":"status","name":"compaction","state":"started"}` + "\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(startupLimit + time.Second)
	if _, err := e.m.advanceTransfer(e.ctx, info.ID, state.ID); err != nil {
		t.Fatal(err)
	}
	got, err := e.m.TransferStatus(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != "starting" || !got.CompactionExtended {
		t.Fatalf("compaction did not extend the startup deadline: %+v", got)
	}
	if !got.StartupDeadline.After(now.Add(30 * time.Second)) {
		t.Fatalf("deadline did not move for compaction: %+v", got)
	}
	held.lock.Close()
	e.m.Spawner = runner
	if _, err := e.m.Store.UpdateTurn(info.ID, 2, func(turn *Turn) {
		turn.Status, turn.Ended = TurnOK, *now
	}); err != nil {
		t.Fatal(err)
	}
}

// A Pi model that thinks through a huge resumed history streams deltas for
// minutes before its first completed message. The streamed content is model
// output, so the startup window completes the transfer instead of aborting
// a healthy turn (D-434).
func TestPiTransferStreamedThinkingIsStartupOutput(t *testing.T) {
	e, now, info, state, held, runner := nativePiTransferStarting(t)
	if _, err := e.m.Store.UpdateTurn(info.ID, 2, func(turn *Turn) {
		turn.OutputDialect = workeroutput.DialectPiJSON
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := e.m.Store.RawOutputPath(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(raw, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString(`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","contentIndex":0,"delta":"I'm resuming after a model transfer."}}` + "\n")
	if cerr := file.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	// The turn keeps running under its own governance once the transfer sees
	// output, so the test releases the runner lock the way retirement would.
	held.lock.Close()
	e.m.Spawner = runner
	*now = now.Add(startupLimit + time.Second)
	if _, err := e.m.advanceTransfer(e.ctx, info.ID, state.ID); err != nil {
		t.Fatal(err)
	}
	got, err := e.m.TransferStatus(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != "completed" || got.Ended.IsZero() {
		t.Fatalf("streamed thinking did not complete the startup window: %+v", got)
	}
	if _, err := e.m.Store.UpdateTurn(info.ID, 2, func(turn *Turn) {
		turn.Status, turn.Ended = TurnOK, *now
	}); err != nil {
		t.Fatal(err)
	}
}

// A native resume that still produces no output after its deadline falls back
// to the written handoff instead of failing the whole transfer.
func TestPiTransferStartupTimeoutUsesTheHandoff(t *testing.T) {
	e, now, info, state, held, runner := nativePiTransferStarting(t)
	held.lock.Close()
	e.m.Spawner = runner
	*now = now.Add(startupLimit + time.Second)
	if _, err := e.m.advanceTransfer(e.ctx, info.ID, state.ID); err != nil {
		t.Fatal(err)
	}
	got, err := e.m.TransferStatus(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != "stopping" || got.Source != "transcript" || !strings.Contains(got.Reason, "no model output") {
		t.Fatalf("startup timeout did not fall back: %+v", got)
	}
}

// A due pre-turn compact gives the transfer a double startup window. A compaction
// event a few seconds after launch must add to that window, not replace it
// with a shorter one.
func TestPiTransferCompactionExtendsADueDeadline(t *testing.T) {
	e, now, info, state, held, runner := nativePiTransferStarting(t, func(p *profile.Profile) {
		p.Compact = &profile.Compact{Tokens: 1}
	})
	current, err := e.m.TransferStatus(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	initial := current.StartupDeadline
	if initial.Sub(*now) < 2*startupLimit {
		t.Fatalf("a due pre-turn compact did not get a longer startup window: %v", initial.Sub(*now))
	}
	normalized, err := e.m.Store.NormalizedPath(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(normalized), 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(normalized, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"v":1,"kind":"status","name":"compaction","state":"started"}` + "\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(2 * time.Second)
	if _, err := e.m.advanceTransfer(e.ctx, info.ID, state.ID); err != nil {
		t.Fatal(err)
	}
	got, err := e.m.TransferStatus(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CompactionExtended || !got.StartupDeadline.Equal(initial.Add(startupLimit)) {
		t.Fatalf("compaction deadline = %v, want the initial %v plus %v: %+v", got.StartupDeadline, initial, startupLimit, got)
	}
	held.lock.Close()
	e.m.Spawner = runner
	if _, err := e.m.Store.UpdateTurn(info.ID, 2, func(turn *Turn) {
		turn.Status, turn.Ended = TurnOK, *now
	}); err != nil {
		t.Fatal(err)
	}
}
