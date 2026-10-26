package subagent

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/state"
	"dyna-agent/internal/workeroutput"
)

// A runner call writes the session of its caller, and only a session's own
// process may make it: the call never names a session (item 3).
func TestRunnerCallsTakeTheSessionFromTheCaller(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("RESPOND: done")
	e.wait(info.ID, 1)
	if err := e.m.RunnerSetPid(context.Background(), 4242); err == nil {
		t.Fatal("a call with no session caller wrote a session")
	}
	ctx := authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Session: info.ID})
	if err := e.m.RunnerSetPid(ctx, 4242); err != nil {
		t.Fatal(err)
	}
	if meta, err := e.m.Store.ReadMeta(info.ID); err != nil || meta.Pid != 4242 {
		t.Fatalf("pid = %d, %v; want 4242 on the caller's session", meta.Pid, err)
	}
}

// Unsupported runner methods are final refusals. Detached runners must not
// change metadata locally when an older engine cannot serve their writes.
func TestRunnerRefusesAnEngineWithoutRunnerCalls(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("RESPOND: done")
	e.wait(info.ID, 1)
	before, err := e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "engine.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"the engine has no method"}`))
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	e.m.Engine = engineclient.New(path)
	w := e.m.runnerWrites(info.ID, 2)
	w.pid(4242)
	pinCtx, cancelPin := context.WithTimeout(t.Context(), 30*time.Second)
	err = w.runtimePin(pinCtx, runtime.Pin{Generation: "g1"})
	cancelPin()
	if err == nil || err.Error() != "the engine has no method" {
		t.Fatalf("runtime pin = %v; want the final method refusal", err)
	}
	w.harnessSession("h-1")
	w.finish(RunnerFinish{Turn: 2, Usage: workeroutput.Usage{InputTokens: 7}})
	closeCtx, cancelClose := context.WithTimeout(t.Context(), 30*time.Second)
	w.close(closeCtx)
	cancelClose()
	after, err := e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Pid != before.Pid || after.HarnessSession != before.HarnessSession || !reflect.DeepEqual(after.RuntimePin, before.RuntimePin) || !reflect.DeepEqual(after.Usage, before.Usage) {
		t.Fatal("unsupported engine methods changed local runner metadata")
	}
}

// The engine stops after a turn start commits but before it stores the
// call's result, and the runner sends it again with the same request ID.
// The ID was recorded with the turn row, so the resend writes nothing: one
// steer for the post, and one line for each event.
func TestTurnStartResentAfterACrashWritesOnce(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("RESPOND: done")
	e.wait(info.ID, 1)
	if err := state.MigrateRequests(filepath.Dir(e.m.store().root())); err != nil {
		t.Fatal(err)
	}
	ctx := state.ForRequest(authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Session: info.ID}), "runner/1")
	start := TurnStart{Turn: 1, Running: true, Drained: []DrainedPost{{Steer: Steer{ID: "msg_once", TS: 1, Message: "hello"}, Owed: "work:lead"}}}
	if err := e.m.RunnerTurnStart(ctx, start); err != nil {
		t.Fatal(err)
	}
	resend := state.ForRequest(authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Session: info.ID}), "runner/1")
	if err := e.m.RunnerTurnStart(resend, start); !errors.Is(err, state.ErrRepeated) {
		t.Fatalf("resend = %v; want ErrRepeated, which the engine answers as done", err)
	}
	turn, err := e.m.Store.ReadTurn(info.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	steers := 0
	for _, s := range turn.Steering {
		if s.ID == "msg_once" {
			steers++
		}
	}
	if steers != 1 || turn.Status != TurnRunning || !slices.Contains(turn.ReportTo, "work:lead") {
		t.Fatalf("turn after a resend: %d steers, status %q, owes %v; want 1, running, work:lead", steers, turn.Status, turn.ReportTo)
	}
	events, err := e.m.Store.Events(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	lines := map[JournalKind]int{}
	for _, ev := range events {
		switch payload := ev.Payload.(type) {
		case *RoomDrainedJournal:
			if payload.Message == "msg_once" {
				lines[JournalRoomDrained]++
			}
		case *TurnStartJournal:
			if payload.Turn == 1 {
				lines[JournalTurnStart]++
			}
		}
	}
	// The first run of turn 1 also logged a turn_start.
	if lines[JournalRoomDrained] != 1 || lines[JournalTurnStart] != 2 {
		t.Fatalf("event lines %v; want one room_drained and one more turn_start", lines)
	}
}

// A failed turn keeps the worker's stderr tail on its record as detail,
// apart from the error text the failure card shows as its cause (D-108).
func TestFailedTurnKeepsWorkerStderrAsDetail(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("RESPOND: done")
	e.wait(info.ID, 1)
	warning := `Warning: Model "muse-wonder-full-1.0" not found for provider "meta". Using custom model id.`
	cursor := int64(1)
	end := TurnEnd{Turn: 1, Status: TurnError,
		Error:        "worker pi (model muse-wonder-full-1.0) failed to start: no harness event within 2m0s of startup",
		WorkerStderr: warning, Ended: time.Now(), FallbackCutoff: &cursor}
	if err := e.m.endTurn(context.Background(), info.ID, end); err != nil {
		t.Fatal(err)
	}
	turn, err := e.m.Store.ReadTurn(info.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if turn.Status != TurnError || strings.Contains(turn.Error, warning) {
		t.Fatalf("failed turn glued stderr into its cause: %q", turn.Error)
	}
	if turn.WorkerStderr != warning {
		t.Fatalf("failed turn lost its worker stderr detail: %q", turn.WorkerStderr)
	}
}

// A turn stopped just before its running write still records the posts it
// drained: they were claimed in the queue, so a turn record must hold them.
func TestStoppedTurnStartKeepsTheDrainedPosts(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("RESPOND: done")
	e.wait(info.ID, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := e.m.runnerWrites(info.ID, 1)
	start := TurnStart{Turn: 1, Drained: []DrainedPost{{Steer: Steer{ID: "msg_kept", TS: 1, Message: "hello"}}}}
	if err := w.start(ctx, start); err == nil {
		t.Fatal("running write under a stopped turn succeeded")
	}
	turn, err := e.m.Store.ReadTurn(info.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	kept := slices.ContainsFunc(turn.Steering, func(s Steer) bool { return s.ID == "msg_kept" })
	if !kept || turn.Status == TurnRunning {
		t.Fatalf("stopped turn start: drained post kept %v, status %q; want kept and not running", kept, turn.Status)
	}
}

// The room's root seat is a session too: its own runner marks its turn
// running. The engine gives it the Root role (SessionActor), which the
// runner calls refused before.
func TestRootRunnerMarksItsTurnRunning(t *testing.T) {
	for _, role := range []authority.Role{authority.Root} {
		t.Run(string(role), func(t *testing.T) {
			e := newTestEnv(t)
			info, _ := e.start("RESPOND: done")
			e.wait(info.ID, 1)
			ctx := authority.WithActor(context.Background(), authority.Actor{Role: role, Session: info.ID, Room: info.RoomID})
			if err := e.m.RunnerTurnStart(ctx, TurnStart{Turn: 1, Running: true}); err != nil {
				t.Fatalf("%s runner's turn start: %v", role, err)
			}
			if turn, err := e.m.Store.ReadTurn(info.ID, 1); err != nil || turn.Status != TurnRunning {
				t.Fatalf("turn = %q, %v; want running", turn.Status, err)
			}
			// A call never names a session: a root runner writes
			// only the session it was launched for, never another one.
			other, _ := e.start("RESPOND: other")
			e.wait(other.ID, 1)
			if err := e.m.RunnerSetPid(ctx, 4242); err != nil {
				t.Fatal(err)
			}
			if meta, err := e.m.Store.ReadMeta(other.ID); err != nil || meta.Pid == 4242 {
				t.Fatalf("other session's pid = %d, %v; a %s runner wrote another session", meta.Pid, err, role)
			}
			noSession := authority.WithActor(context.Background(), authority.Actor{Role: role, Room: info.RoomID})
			if err := e.m.RunnerSetPid(noSession, 1); err == nil {
				t.Fatalf("a %s caller with no session wrote a session record", role)
			}
		})
	}
}

// Two runner processes for the same turn send different request IDs, so
// the engine never takes the second runner's writes for repeats of the
// first one's.
func TestTwoRunnersSendDistinctRequestIDs(t *testing.T) {
	var mu sync.Mutex
	var ids []string
	path := filepath.Join(t.TempDir(), "engine.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ids = append(ids, r.Header.Get(callspec.RequestHeader))
		mu.Unlock()
		_, _ = w.Write([]byte("null"))
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	m := &Manager{Engine: engineclient.New(path)}
	for range 2 {
		w := m.runnerWrites("ses_twice", 1)
		w.pid(1)
		w.close(t.Context())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ids) != 2 || ids[0] == "" || ids[0] == ids[1] {
		t.Fatalf("request IDs = %q; want two distinct IDs", ids)
	}
}
