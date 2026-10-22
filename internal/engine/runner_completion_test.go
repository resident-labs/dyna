package engine

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

const (
	completionParentID = "ses_20261021-013600-a00001"
	completionChildID  = "ses_20261021-013600-a00002"
	completionOtherID  = "ses_20261021-013600-a00003"
)

// RunnerCompleteTurn is served from the runner binding table. The caller's
// session and request ID are checked, a terminal turn stores pending intent,
// and a repeat or replay keeps a single completion post.
func TestRunnerCompleteTurnChecksCallerAndReportsOnce(t *testing.T) {
	h := newCompletionHarness(t)
	for _, name := range []string{"RunnerTurnEnd", "RunnerCompleteTurn"} {
		if !h.bound[name] {
			t.Fatalf("runner bindings omit %s", name)
		}
	}

	endID := completionChildID + "/end"
	completeID := completionChildID + "/complete"
	payload := completionEndPayload(t)

	h.setCaller(websvc.Caller{Kind: websvc.Owner})
	if err := h.call("RunnerCompleteTurn", completionChildID+"/owner", []any{1}); err == nil || !strings.Contains(err.Error(), "session's own turn runner") {
		t.Fatalf("owner completion = %v; want the session-runner refusal", err)
	}

	h.setCaller(websvc.Caller{Kind: websvc.Session, Session: completionChildID})
	if err := h.call("RunnerCompleteTurn", completionOtherID+"/stolen", []any{1}); err == nil || !strings.Contains(err.Error(), "does not belong to session") {
		t.Fatalf("foreign request ID = %v; want the session prefix refusal", err)
	}

	h.setCaller(websvc.Caller{Kind: websvc.Session, Session: completionOtherID})
	if err := h.call("RunnerCompleteTurn", completionOtherID+"/complete", []any{1}); err == nil {
		t.Fatal("another session completed this caller's own missing turn")
	}
	if turn := h.turn(); turn.Status != subagent.TurnRunning || turn.ReportPending || turn.Reported {
		t.Fatalf("child turn after a foreign caller = %+v; want it still running", turn)
	}

	h.setCaller(websvc.Caller{Kind: websvc.Session, Session: completionChildID})
	if err := h.call("RunnerTurnEnd", endID, []any{payload}); err != nil {
		t.Fatal(err)
	}
	turn := h.turn()
	if turn.Status != subagent.TurnOK || !turn.ReportPending || turn.Reported {
		t.Fatalf("terminal turn = %+v; want pending completion intent", turn)
	}
	if err := h.call("RunnerTurnEnd", endID, []any{payload}); err != nil {
		t.Fatalf("turn-end replay = %v", err)
	}
	if turn = h.turn(); turn.Status != subagent.TurnOK || !turn.ReportPending || turn.Reported {
		t.Fatalf("turn after end replay = %+v; want the same pending intent", turn)
	}

	if err := h.call("RunnerCompleteTurn", completeID, []any{1}); err != nil {
		t.Fatal(err)
	}
	posted := waitCompletion(t, h.rooms, h.room.ID, h.childAddr)
	if posted.Turn != 1 || len(posted.To) != 1 || posted.To[0] != h.parentAddr || len(posted.Via) != 1 || posted.Via[0] != "@parent" || posted.OwnerReport {
		t.Fatalf("completion post = %+v; want one parent report", posted)
	}
	if err := h.call("RunnerCompleteTurn", completeID, []any{1}); err != nil {
		t.Fatalf("completion replay = %v", err)
	}
	if again := h.completeStored(); again.Message.ID != "" {
		t.Fatalf("repeat CompleteSessionTurn = %+v; want no second report", again.Message)
	}
	if err := h.call("RunnerCompleteTurn", completionChildID+"/complete-again", []any{1}); err != nil {
		t.Fatalf("second completion request = %v", err)
	}
	if again := h.completeStored(); again.Message.ID != "" {
		t.Fatalf("fresh completion request = %+v; want no second report", again.Message)
	}
	if n := completionCount(t, h.rooms, h.room.ID, h.childAddr); n != 1 {
		t.Fatalf("completion posts = %d; want 1", n)
	}
	if turn = h.turn(); !turn.Reported || turn.ReportPending {
		t.Fatalf("stored turn after replay = %+v; want one acknowledged report", turn)
	}
}

type completionHarness struct {
	t          *testing.T
	poseMu     sync.RWMutex
	pose       websvc.Caller
	svc        *notify.Service
	rooms      *room.Store
	room       room.Record
	childAddr  string
	parentAddr string
	bound      map[string]bool
}

func newCompletionHarness(t *testing.T) *completionHarness {
	t.Helper()
	data := storetest.Isolate(t)
	if err := MigrateStores(data); err != nil {
		t.Fatal(err)
	}
	if err := state.MigrateRequests(data); err != nil {
		t.Fatal(err)
	}
	svc := roomsvc.New(nil).Notify
	mgr := svc.Sessions
	ctx, cancel := context.WithCancel(t.Context())
	mgr.Workers = subagent.NewWorkers(ctx, t.Logf)
	configureTurnReports(ctx, svc)
	rec, err := svc.Rooms.Create(room.CreateInput{Name: "completion", CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	parent := subagent.Meta{
		ID: completionParentID, Name: "lead", RoomID: rec.ID, Room: rec.Name,
		Profile: "mock", Dir: t.TempDir(), Depth: 1, Created: now, LastActivity: now,
		Status: subagent.StatusRunning, Resumable: true, HarnessSession: "parent-harness",
	}
	child := subagent.Meta{
		ID: completionChildID, Name: "decoder", RoomID: rec.ID, Room: rec.Name,
		Profile: "mock", Dir: t.TempDir(), Depth: 2, ParentSession: parent.ID,
		Created: now, LastActivity: now, Turns: 1, Status: subagent.StatusIdle, Resumable: true,
	}
	other := subagent.Meta{
		ID: completionOtherID, Name: "other", RoomID: rec.ID, Room: rec.Name,
		Profile: "mock", Dir: t.TempDir(), Depth: 1, Created: now, LastActivity: now,
		Status: subagent.StatusIdle, Resumable: true,
	}
	recordtest.Write(t, mgr.Store.Root, parent.ID, parent)
	recordtest.Write(t, mgr.Store.Root, child.ID, child)
	recordtest.Write(t, mgr.Store.Root, other.ID, other)
	// An ok turn with an empty report list ends silently. The parent address
	// is the obligation a real first turn records.
	raw, err := json.Marshal(subagent.Turn{
		N: 1, Status: subagent.TurnRunning, Prompt: "ready", Created: now,
		ReportTo: []string{parent.Address()},
	})
	if err != nil {
		t.Fatal(err)
	}
	dir, err := mgr.Store.Dir(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "turns"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "turns", "1.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Store.ImportLegacyTurn(child.ID, 1); err != nil {
		t.Fatal(err)
	}

	h := &completionHarness{
		t: t, svc: svc, rooms: svc.Rooms, room: rec,
		childAddr: child.Address(), parentAddr: parent.Address(), bound: map[string]bool{},
	}
	bindings := RunnerBindings(func() *subagent.Manager { return mgr })
	for _, binding := range bindings {
		h.bound[binding.Name] = true
	}
	calls, err := websvc.Bind(bindings)
	if err != nil {
		t.Fatal(err)
	}
	callers := &socketCallers{sessions: mgr.Store}
	callers.actForCallers(calls)
	db, err := database.Open(store.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	applyOnce(calls, state.For(db), t.Logf)
	enginetest.ServeCallers(t, store.DataDir(), func(net.Conn) (websvc.Caller, error) {
		h.poseMu.RLock()
		defer h.poseMu.RUnlock()
		return h.pose, nil
	}, calls)
	t.Cleanup(func() {
		cancel()
		if left := mgr.Workers.Wait(30 * time.Second); len(left) != 0 {
			t.Errorf("completion workers did not stop: %v", left)
		}
	})
	return h
}

func (h *completionHarness) setCaller(caller websvc.Caller) {
	h.poseMu.Lock()
	h.pose = caller
	h.poseMu.Unlock()
}

func (h *completionHarness) call(method, requestID string, args []any) error {
	h.t.Helper()
	client := engineclient.New("", engineclient.RestartWait(0), engineclient.Notices(io.Discard))
	ctx := h.t.Context()
	if requestID != "" {
		ctx = callspec.WithRequest(ctx, requestID)
	}
	return client.Call(ctx, method, args, nil)
}

// completeStored replays CompleteSessionTurn. The failure bound only stops
// a held report lock from hanging the test. A free lock returns at once.
func (h *completionHarness) completeStored() notify.Result {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(h.t.Context(), 30*time.Second)
	defer cancel()
	result, err := h.svc.CompleteSessionTurn(ctx, completionChildID, 1)
	if err != nil {
		h.t.Fatalf("CompleteSessionTurn: %v", err)
	}
	return result
}

func (h *completionHarness) turn() subagent.Turn {
	h.t.Helper()
	turn, err := h.svc.Sessions.Store.ReadTurn(completionChildID, 1)
	if err != nil {
		h.t.Fatal(err)
	}
	return turn
}

// completionEndPayload is a terminal RunnerTurnEnd argument. The pending
// flag is added as JSON so the test does not name the typed field.
func completionEndPayload(t *testing.T) map[string]any {
	t.Helper()
	raw, err := json.Marshal(subagent.TurnEnd{Turn: 1, Status: subagent.TurnOK, Ended: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	payload["completionPending"] = true
	return payload
}

func waitCompletion(t *testing.T, rooms *room.Store, roomID, from string) room.Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var found room.Message
	err := rooms.Follow(ctx, roomID, func(msg room.Message) (bool, error) {
		if msg.Kind == "completed" && msg.From == from {
			found = msg
			return true, nil
		}
		return false, nil
	})
	if err != nil || found.ID == "" {
		t.Fatalf("completion post from %s: %v", from, err)
	}
	return found
}

func completionCount(t *testing.T, rooms *room.Store, roomID, from string) int {
	t.Helper()
	msgs, err := rooms.Messages(roomID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, msg := range msgs {
		if msg.Kind == "completed" && msg.From == from {
			n++
		}
	}
	return n
}
