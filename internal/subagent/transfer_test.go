package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/mode"
	prstate "dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/workeroutput"
)

func transferEnv(t *testing.T) (*testEnv, *time.Time) {
	t.Helper()
	old, newp := mockProfile("mock"), mockProfile("next")
	old.Model, newp.Model = "old-model", "new-model"
	e := newTestEnv(t, old, newp)
	now := time.Now()
	e.m.TransferNow = func() time.Time { return now }
	e.m.TransferLaunch = func(*Manager, string, string) error { return nil }
	return e, &now
}
func advance(t *testing.T, e *testEnv, state TransferState, steps int) {
	t.Helper()
	for range steps {
		if _, err := e.m.advanceTransfer(e.ctx, state.Session, state.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.m.TickSessions(e.ctx); err != nil {
			t.Fatal(err)
		}
	}
}
func completeTransfer(t *testing.T, e *testEnv, state TransferState) {
	t.Helper()
	advance(t, e, state, 3)
	e.wait(state.Session, 0)
	advance(t, e, state, 1)
	got, err := e.m.TransferStatus(state.Session)
	if err != nil || got.Phase != "completed" {
		t.Fatalf("not completed: %+v %v", got, err)
	}
}

func TestTransferCooperativePreservesIdentityChildrenAndPRs(t *testing.T) {
	e, _ := transferEnv(t)
	lead, _ := e.start("SLEEP:1 RESPOND: continue the decoder task")
	lead, first := e.wait(lead.ID, 1)
	stampParent(t, lead)
	child, _ := e.start("SLEEP:1 RESPOND: child")
	child, _ = e.wait(child.ID, 1)
	writePRLink(t, lead, "author", "open", true)
	actual := t.TempDir()
	state, err := e.m.TransferContext(context.Background(), lead.ID, TransferOptions{Profile: "next", WaitSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.m.SubmitHandoff(context.Background(), lead.ID, state.ID, "Goal: finish decoder. Changed parser.go. Next: run tests. Branch: feature/decoder.", actual, "feature/decoder"); err != nil {
		t.Fatal(err)
	}
	beforeDepth := os.Getenv(mode.EnvDepth)
	completeTransfer(t, e, state)
	meta, _ := e.m.Store.ReadMeta(lead.ID)
	if meta.ID != lead.ID || meta.Name != lead.Name || meta.ParentSession != lead.ParentSession || meta.Depth != lead.Depth || meta.RoomID != lead.RoomID || meta.Profile != "next" || meta.Model != "new-model" || meta.Dir != actual || meta.HarnessSession == first.HarnessSession {
		t.Fatalf("identity or target changed incorrectly: %+v", meta)
	}
	if os.Getenv(mode.EnvDepth) != beforeDepth {
		t.Fatal("transfer changed the caller's process environment")
	}
	currentChild, _ := e.m.Store.ReadMeta(child.ID)
	if currentChild.ParentSession != lead.ID || currentChild.Depth != child.Depth {
		t.Fatalf("child changed: %+v", currentChild)
	}
	links, err := e.m.TrackedPRs(lead.ID)
	if err != nil || len(links) != 1 || !links[0].Active() {
		t.Fatalf("PR continuity: %+v %v", links, err)
	}
	old, _ := e.m.Store.ReadTurn(lead.ID, 1)
	next, _ := e.m.Store.ReadTurn(lead.ID, 2)
	if old.HarnessSession != first.HarnessSession || old.Profile != "mock" || old.Model != "old-model" || next.Profile != "next" || next.Model != "new-model" || next.TransferID != state.ID {
		t.Fatalf("turn attribution lost: %+v %+v", old, next)
	}
	for _, want := range []string{"Review this source before acting", "dyna subagent transfer status " + lead.ID, "dyna subagent history " + lead.ID} {
		if !strings.Contains(next.Prompt, want) {
			t.Fatalf("replacement context lacks %q", want)
		}
	}
	// A repeat that names the profile and model the session already runs
	// starts a fresh conversation; the completed record only described the
	// move that already happened.
	again, err := e.m.TransferContext(context.Background(), lead.ID, TransferOptions{Profile: "next", WaitSeconds: 30})
	if err != nil || again.ID == state.ID || again.Profile != "next" {
		t.Fatalf("repeat did not start a fresh conversation: %+v %v", again, err)
	}
	if after, _ := e.m.Store.ReadMeta(lead.ID); after.Turns != 2 {
		t.Fatalf("a repeat before its handoff ran a turn: turns = %d", after.Turns)
	}
}

// A session cannot be transferred to the profile and model it already runs:
// the move would do nothing. It starts another conversation instead, which is
// the recovery for a harness thread that stopped answering.
func TestTransferToTheCurrentProfileStartsAnotherConversation(t *testing.T) {
	e, _ := transferEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: first")
	info, _ = e.wait(info.ID, 1)
	first, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "next", WaitSeconds: 0})
	if err != nil {
		t.Fatal(err)
	}
	completeTransfer(t, e, first)
	meta, _ := e.m.Store.ReadMeta(info.ID)
	turns, harness := meta.Turns, meta.HarnessSession

	again, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "next", WaitSeconds: 0})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID == first.ID || again.Profile != "next" || again.Phase != "waiting" {
		t.Fatalf("same-profile transfer = %+v, want a new attempt", again)
	}
	completeTransfer(t, e, again)
	meta, _ = e.m.Store.ReadMeta(info.ID)
	if meta.Turns != turns+1 || meta.HarnessSession == harness {
		t.Fatalf("no second conversation: turns %d -> %d, harness %q", turns, meta.Turns, meta.HarnessSession)
	}
}

func TestTransferProviderFailureUsesRecentMessagesAndPRWorktreeImmediately(t *testing.T) {
	e, _ := transferEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: original task")
	info, _ = e.wait(info.ID, 1)
	_, err := e.m.Store.UpdateTurn(info.ID, 1, func(t *Turn) { t.Status = TurnError; t.Error = "429 provider limit; resets at 17:00" })
	if err != nil {
		t.Fatal(err)
	}
	actual := t.TempDir()
	writePRLink(t, info, "author", "open", true)
	root := prstate.Root(filepath.Dir(e.m.Store.Root))
	st, err := prstate.Read(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range st.Records {
		r.Dir = info.Dir
		r.LookupDir = actual
		r.Head = "abc123"
	}
	if err = prstate.Write(root, st); err != nil {
		t.Fatal(err)
	}
	norm, _ := e.m.Store.NormalizedPath(info.ID)
	f, _ := os.OpenFile(norm, os.O_APPEND|os.O_WRONLY, 0600)
	event, _ := json.Marshal(workeroutput.Event{Kind: "message", Role: "assistant", Text: "I edited parser.go; next run go test ./decoder."})
	f.Write(append(event, '\n'))
	f.Close()
	if _, err := e.m.rooms().Post(info.RoomID, room.PostInput{From: info.Address(), To: []string{info.Room}, Text: "Task worktree is the decoder branch."}); err != nil {
		t.Fatal(err)
	}
	state, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "next", WaitSeconds: 120})
	if err != nil {
		t.Fatal(err)
	}
	advance(t, e, state, 1)
	current, _ := e.m.TransferStatus(info.ID)
	if current.Phase != "stopping" || current.Posted {
		t.Fatalf("failed agent was asked to respond: %+v", current)
	}
	advance(t, e, state, 2)
	e.wait(info.ID, 0)
	advance(t, e, state, 1)
	meta, _ := e.m.Store.ReadMeta(info.ID)
	turn, _ := e.m.Store.ReadTurn(info.ID, 2)
	if meta.Dir != actual || meta.Transfer.Source != "transcript" {
		t.Fatalf("fallback directory: %+v", meta.Transfer)
	}
	if !strings.Contains(turn.Prompt, "dyna subagent history "+info.ID) || strings.Contains(turn.Prompt, "--to-turn") || strings.Contains(turn.Prompt, "I edited parser.go") {
		t.Fatalf("replacement must reference archived evidence: %s", turn.Prompt)
	}
	packet, _, err := e.m.handoffPacket(meta, *meta.Transfer)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"429 provider limit", "abc123", "head: abc123", actual, "--search '<pattern>'"} {
		if !strings.Contains(packet, want) {
			t.Fatalf("saved evidence lacks %q:\n%s", want, packet)
		}
	}
	for _, gone := range []string{"I edited parser.go", "decoder branch"} {
		if strings.Contains(packet, gone) {
			t.Fatalf("packet inlined %q instead of pointing at the old context:\n%s", gone, packet)
		}
	}
}

func TestTransferNoResponseUsesInjectedDeadlineAndCancel(t *testing.T) {
	e, now := transferEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: old task")
	info, _ = e.wait(info.ID, 1)
	state, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "next", WaitSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	// Keep the handoff turn queued. No real clock or account is involved.
	// Keep retirement tracking when the temporary spawner is removed.
	runner := e.m.Spawner
	held := &heldTurnSpawner{}
	e.m.Spawner = held
	advance(t, e, state, 1)
	before, _ := e.m.TransferStatus(info.ID)
	if before.Phase != "waiting" {
		t.Fatalf("wait skipped: %+v", before)
	}
	*now = now.Add(31 * time.Second)
	advance(t, e, state, 1)
	held.lock.Close()
	e.m.Spawner = runner
	advance(t, e, state, 2)
	e.wait(info.ID, 0)
	advance(t, e, state, 1)
	got, _ := e.m.TransferStatus(info.ID)
	if got.Phase != "completed" || got.Source != "transcript" || got.Reason != "handoff wait ended" {
		t.Fatalf("deadline fallback: %+v", got)
	}
	// A new target creates a new attempt; cancellation leaves the current model intact.
	next, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "mock", WaitSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	if cancelled, err := e.m.CancelTransfer(context.Background(), info.ID); err != nil || cancelled.Phase != "cancelled" {
		t.Fatalf("cancel = %+v %v", cancelled, err)
	}
	advance(t, e, next, 1)
	meta, _ := e.m.Store.ReadMeta(info.ID)
	if meta.Profile != "next" {
		t.Fatal("cancel changed the current profile")
	}
	if err := e.m.SubmitHandoff(context.Background(), info.ID, next.ID, "late handoff", "", ""); err == nil {
		t.Fatal("cancelled transfer accepted late handoff")
	}
}

type failedTransferSpawner struct{}

func (failedTransferSpawner) Spawn(_ *Manager, _ string, _ int, _ *os.File) error {
	return errors.New("injected runner start failure")
}

func TestTransferStartupFailureRestoresPriorResumeAndRetryIsRecoverable(t *testing.T) {
	e, _ := transferEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: old")
	info, _ = e.wait(info.ID, 1)
	state, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "next", WaitSeconds: 0})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.m.SubmitHandoff(context.Background(), info.ID, state.ID, "Preserve the handoff across startup failure.", info.Dir, "retry-branch"); err != nil {
		t.Fatal(err)
	}
	// Keep retirement tracking when the temporary spawner is removed.
	runner := e.m.Spawner
	e.m.Spawner = failedTransferSpawner{}
	advance(t, e, state, 2)
	if _, err := e.m.advanceTransfer(e.ctx, info.ID, state.ID); err == nil {
		t.Fatal("startup failure hidden")
	}
	meta, _ := e.m.Store.ReadMeta(info.ID)
	if meta.Profile != info.Profile || meta.Model != info.Model || meta.HarnessSession != info.HarnessSession || meta.Dir != info.Dir || !meta.Closed.IsZero() || meta.Transfer.Phase != "failed" {
		t.Fatalf("old state not recoverable: %+v", meta)
	}
	old, _ := e.m.Store.ReadTurn(info.ID, 1)
	failed, _ := e.m.Store.ReadTurn(info.ID, 2)
	if old.Status != TurnOK || failed.Status != TurnError || failed.Profile != "next" {
		t.Fatalf("history lost: %+v %+v", old, failed)
	}
	again, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "next", WaitSeconds: 0})
	if err != nil || again.ID != state.ID {
		t.Fatalf("failed repeat: %+v %v", again, err)
	}
	e.m.Spawner = runner
	retry, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "next", WaitSeconds: 0, Retry: true})
	if err != nil || retry.ID == state.ID {
		t.Fatalf("retry: %+v %v", retry, err)
	}
	if retry.WrittenHandoff != "Preserve the handoff across startup failure." {
		t.Fatal("retry lost the written handoff")
	}
	completeTransfer(t, e, retry)
}

func TestTransferWorkerRecoveryAndStartupTimeout(t *testing.T) {
	e, now := transferEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: old")
	info, _ = e.wait(info.ID, 1)
	launches := 0
	e.m.TransferLaunch = func(*Manager, string, string) error { launches++; return errors.New("worker unavailable") }
	state, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "next", WaitSeconds: 0})
	if err == nil || state.ID == "" {
		t.Fatalf("unsaved launch failure: %+v %v", state, err)
	}
	same, _ := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "next", WaitSeconds: 0})
	if same.ID != state.ID || launches != 2 {
		t.Fatal("worker recovery duplicated transfer")
	}
	held := &heldTurnSpawner{}
	e.m.Spawner = held
	advance(t, e, state, 3)
	*now = now.Add(61 * time.Second)
	held.lock.Close()
	if _, err := e.m.advanceTransfer(e.ctx, info.ID, state.ID); err == nil || !strings.Contains(err.Error(), "no model output") {
		t.Fatalf("startup timeout: %v", err)
	}
	meta, _ := e.m.Store.ReadMeta(info.ID)
	if meta.Profile != info.Profile || meta.HarnessSession != info.HarnessSession {
		t.Fatal("timeout did not restore old model")
	}
}

func TestTransferCancelledWorkerCannotCreateReplacement(t *testing.T) {
	e, _ := transferEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: old")
	info, _ = e.wait(info.ID, 1)
	state, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "next", WaitSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.CancelTransfer(context.Background(), info.ID); err != nil {
		t.Fatal(err)
	}
	e.m.TransferSleep = func(context.Context, time.Duration) error { return errors.New("unexpected wait") }
	if err := e.m.runTransfer(e.ctx, info.ID, state.ID); err != nil {
		t.Fatal(err)
	}
	meta, _ := e.m.Store.ReadMeta(info.ID)
	if meta.Turns != 1 {
		t.Fatal("cancelled worker created a turn")
	}
	if _, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "missing"}); err == nil {
		t.Fatal("accepted missing profile")
	}
	e.doneGoal(info)
	if _, err := e.m.CloseWithOptions(e.ctx, info.ID, CloseOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "next"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed transfer: %v", err)
	}
}

func TestTransferFinalHandoffHintsAndDurableCoordinatorRecovery(t *testing.T) {
	e, _ := transferEnv(t)
	e.joinRunners()
	info, _ := e.start("SLEEP:1 RESPOND: original")
	info, _ = e.wait(info.ID, 1)
	state, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "next", WaitSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	actual := t.TempDir()
	// Simulate the old model writing a cooperative final answer after delivery.
	if err := e.m.updateTransfer(info.ID, state.ID, func(_ *Meta, t *TransferState) error { t.Posted = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Store.UpdateTurn(info.ID, 1, func(turn *Turn) {
		turn.Result = "[DYNA HANDOFF]\nWorktree: " + actual + "\nBranch: decoder-transfer\nNext: finish parser."
		turn.Ended = time.Now().Add(time.Second)
	}); err != nil {
		t.Fatal(err)
	}
	advance(t, e, state, 1) // adopts the final answer as a written handoff
	advance(t, e, state, 2) // commits the target profile before the replacement exists
	meta, _ := e.m.Store.ReadMeta(info.ID)
	if meta.Transfer.Phase != "starting" || meta.Turns != 1 {
		t.Fatalf("unexpected durable checkpoint: %+v", meta.Transfer)
	}
	// A new manager process can recover that checkpoint without environment edits.
	recovered := *e.m
	recovered.TransferSleep = func(ctx context.Context, _ time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
			return nil
		}
	}
	ctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
	defer cancel()
	if err := recovered.runTransfer(ctx, info.ID, state.ID); err != nil {
		t.Fatal(err)
	}
	meta, _ = e.m.Store.ReadMeta(info.ID)
	if meta.Transfer.Phase != "completed" || meta.Turns != 2 || meta.Dir != actual || !strings.Contains(meta.Transfer.WrittenHandoff, "decoder-transfer") {
		t.Fatalf("recovered checkpoint: %+v", meta.Transfer)
	}
	if err := recovered.runTransfer(ctx, info.ID, state.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := e.m.Store.ReadMeta(info.ID)
	if after.Turns != 2 {
		t.Fatal("recovered coordinator created a duplicate")
	}
}

// A transfer requested mid-turn must wait for that turn to end before it asks
// for a handoff, then give the handoff its full window. Otherwise a long turn
// burns the wait and the session falls back to a transcript handoff.
func TestTransferHoldsHandoffAskUntilRunningTurnEnds(t *testing.T) {
	e, now := transferEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: initial")
	info, _ = e.wait(info.ID, 1)
	// Begin a turn and hold it open so the session is mid-turn.
	held := &heldTurnSpawner{}
	e.m.Spawner = held
	second, err := e.m.beginTurn(info.ID, "RESPOND: running task", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.lock.Close()
	state, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "next", WaitSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	// The in-flight turn runs well past the original handoff window. The ask
	// must not go out, and the transfer must not fall back, while it runs.
	*now = now.Add(2 * time.Minute)
	if _, err := e.m.advanceTransfer(e.ctx, info.ID, state.ID); err != nil {
		t.Fatal(err)
	}
	current, _ := e.m.TransferStatus(info.ID)
	if current.Posted || current.Phase != "waiting" || current.Reason != "" {
		t.Fatalf("ask fired while the turn was still running: %+v", current)
	}
	// The turn ends; the ask goes out and the full window starts from there.
	if _, err := e.m.Store.UpdateTurn(info.ID, second.N, func(turn *Turn) {
		turn.Status, turn.Ended = TurnOK, *now
	}); err != nil {
		t.Fatal(err)
	}
	held.lock.Close()
	if _, err := e.m.advanceTransfer(e.ctx, info.ID, state.ID); err != nil {
		t.Fatal(err)
	}
	current, _ = e.m.TransferStatus(info.ID)
	if !current.Posted {
		t.Fatalf("ask was not posted after the turn ended: %+v", current)
	}
	if !current.Deadline.After(now.Add(29 * time.Second)) {
		t.Fatalf("handoff window did not restart after the turn ended: %+v", current)
	}
}

// Regression: if a running turn ends past the original deadline and the
// coordinator never polled while it ran (so the deadline was never pushed out),
// the transfer must still ask for the handoff and give it a full window rather
// than fall back to a transcript handoff.
func TestTransferAsksAfterTurnEndsPastDeadline(t *testing.T) {
	e, now := transferEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: initial")
	info, _ = e.wait(info.ID, 1)
	held := &heldTurnSpawner{}
	e.m.Spawner = held
	second, err := e.m.beginTurn(info.ID, "RESPOND: running task", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.lock.Close()
	state, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "next", WaitSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	// The turn ends past the deadline, with no poll in between, so the deadline
	// was never pushed out while it ran.
	*now = now.Add(time.Minute)
	if _, err := e.m.Store.UpdateTurn(info.ID, second.N, func(turn *Turn) {
		turn.Status, turn.Ended = TurnOK, *now
	}); err != nil {
		t.Fatal(err)
	}
	held.lock.Close()
	if _, err := e.m.advanceTransfer(e.ctx, info.ID, state.ID); err != nil {
		t.Fatal(err)
	}
	current, _ := e.m.TransferStatus(info.ID)
	if !current.Posted || current.Phase != "waiting" || current.Reason != "" {
		t.Fatalf("transfer fell back instead of asking after a late turn: %+v", current)
	}
	if !current.Deadline.After(now.Add(29 * time.Second)) {
		t.Fatalf("handoff window did not start after the late turn ended: %+v", current)
	}
}

// The ask for a handoff is a typed session event from the engine: its card
// says a transfer needs a handoff, and the session reads the full instructions
// as the event's text. The ask wakes the session it is addressed to, and no
// post claims to come from the room's owner.
func TestTransferAskIsATypedSessionEvent(t *testing.T) {
	e, now := transferEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: initial")
	info, _ = e.wait(info.ID, 1)
	held := &heldTurnSpawner{}
	e.m.Spawner = held
	second, err := e.m.beginTurn(info.ID, "RESPOND: running task", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.lock.Close()
	state, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "next", WaitSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	// The turn ends past the deadline, so the ask goes out on this poll.
	*now = now.Add(time.Minute)
	if _, err := e.m.Store.UpdateTurn(info.ID, second.N, func(turn *Turn) {
		turn.Status, turn.Ended = TurnOK, *now
	}); err != nil {
		t.Fatal(err)
	}
	held.lock.Close()
	if _, err := e.m.advanceTransfer(e.ctx, info.ID, state.ID); err != nil {
		t.Fatal(err)
	}
	msg, err := e.m.rooms().Message(info.RoomID, state.PostID)
	if err != nil {
		t.Fatal(err)
	}
	if msg.From != room.EngineSender || msg.Kind != string(room.FamilySession) || msg.Notice.Kind != string(room.SessionTransfer) {
		t.Fatalf("ask is not a typed session event: from=%q kind=%q type=%q", msg.From, msg.Kind, msg.Notice.Kind)
	}
	if !msg.Wake || !msg.Addressed(info.Address()) {
		t.Fatalf("ask does not wake its session: wake=%v to=%v", msg.Wake, msg.To)
	}
	ev, ok := msg.Session()
	if !ok || ev.Type != room.SessionTransfer || ev.Session != info.Address() || ev.Transfer != state.ID || ev.Profile != state.Profile {
		t.Fatalf("ask payload = %+v, %v", ev, ok)
	}
	if !strings.Contains(ev.Detail, "dyna subagent transfer handoff "+info.ID) || !strings.Contains(ev.Detail, state.ID) {
		t.Fatalf("ask lost the instructions: %q", ev.Detail)
	}
	if !strings.HasPrefix(msg.Text, "[DYNA MODEL TRANSFER]") || !strings.Contains(msg.Text, "[DYNA HANDOFF]") {
		t.Fatalf("ask text = %q", msg.Text)
	}
}

func TestTransferWakesInputThatArrivedAfterReplacementTurnEnded(t *testing.T) {
	e, _ := transferEnv(t)
	old, _ := e.start("SLEEP:1 RESPOND: old")
	old, _ = e.wait(old.ID, 1)
	state, err := e.m.TransferContext(context.Background(), old.ID, TransferOptions{Profile: "next", WaitSeconds: 0})
	if err != nil {
		t.Fatal(err)
	}
	advance(t, e, state, 3)
	_, replacement := e.wait(old.ID, 2)
	msg, err := e.m.rooms().Post(old.RoomID, room.PostInput{Wake: true, From: old.Room, To: []string{old.Address()}, Text: "RESPOND: late startup input"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Wake(e.ctx, old.ID, WakeOptions{MessageID: msg.ID}); err == nil {
		t.Fatal("startup should retain input until the switch commits")
	}
	advance(t, e, state, 1)
	_, continued := e.wait(old.ID, 3)
	if continued.Status != TurnOK || continued.HarnessSession != replacement.HarnessSession || !strings.Contains(resultString(continued), "late startup input") {
		t.Fatalf("late input did not resume replacement: %+v", continued)
	}
}

// Drive the real coordinator with an injected clock. The queued handoff turn
// stays under our lock until the selected event, then target startup fails
// deliberately so this test needs no background model or wall-clock wait.
func TestTransferPollingBackoffAndImmediateProgress(t *testing.T) {
	for _, scenario := range []string{"failed", "deadline", "handoff"} {
		t.Run(scenario, func(t *testing.T) {
			e, now := transferEnv(t)
			info, _ := e.start("SLEEP:1 RESPOND: initial task")
			info, _ = e.wait(info.ID, 1)
			if scenario == "failed" {
				if _, err := e.m.Store.UpdateTurn(info.ID, 1, func(turn *Turn) { turn.Status, turn.Error = TurnError, "provider limit" }); err != nil {
					t.Fatal(err)
				}
			}
			state, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "next", WaitSeconds: 30})
			if err != nil {
				t.Fatal(err)
			}
			held := &heldTurnSpawner{}
			if scenario == "failed" {
				e.m.Spawner = failedTransferSpawner{}
			} else {
				e.m.Spawner = held
			}
			var pauses []time.Duration
			e.m.TransferSleep = func(_ context.Context, pause time.Duration) error {
				if _, err := e.m.TickSessions(e.ctx); err != nil {
					t.Fatal(err)
				}
				current, _ := e.m.TransferStatus(info.ID)
				if scenario == "failed" || current.Phase != "waiting" {
					t.Fatalf("delayed phase progress: %s / %s", current.Phase, pause)
				}
				if pause <= 0 || pause > time.Second || now.Add(pause).After(state.Deadline) {
					t.Fatalf("invalid backoff %s at %s", pause, now)
				}
				pauses = append(pauses, pause)
				*now = now.Add(pause)
				if scenario == "handoff" || !now.Before(state.Deadline) {
					if scenario == "handoff" {
						if err := e.m.SubmitHandoff(context.Background(), info.ID, state.ID, "Continue the parser.", "", ""); err != nil {
							t.Fatal(err)
						}
					}
					meta, _ := e.m.Store.ReadMeta(info.ID)
					if _, err := e.m.Store.UpdateTurn(info.ID, meta.Turns, func(turn *Turn) { turn.Status, turn.Ended = TurnStopped, *now }); err != nil {
						t.Fatal(err)
					}
					if held.lock == nil {
						t.Fatal("handoff turn was not created")
					}
					held.lock.Close()
					e.m.Spawner = failedTransferSpawner{}
				}
				return nil
			}
			if err := e.m.runTransfer(e.ctx, info.ID, state.ID); err == nil || !strings.Contains(err.Error(), "injected runner start failure") {
				t.Fatalf("expected target startup failure: %v", err)
			}
			got, _ := e.m.TransferStatus(info.ID)
			switch scenario {
			case "failed":
				if got.Posted || len(pauses) != 0 || !strings.Contains(got.Reason, "provider limit") {
					t.Fatalf("failed agent waited: %+v / %v", got, pauses)
				}
			case "handoff":
				if len(pauses) != 1 || got.Source != "cooperative" || !now.Before(state.Deadline) {
					t.Fatalf("early handoff delayed: %+v / %v", got, pauses)
				}
			case "deadline":
				if !now.Equal(state.Deadline) || got.Reason != "handoff wait ended" || got.Source != "transcript" {
					t.Fatalf("deadline fallback: %+v / %v", got, pauses)
				}
				if len(pauses) > 35 || len(pauses) < 4 || pauses[0] != 200*time.Millisecond || pauses[1] != 400*time.Millisecond || pauses[2] != 800*time.Millisecond {
					t.Fatalf("not bounded backoff: %v", pauses)
				}
			}
			meta, _ := e.m.Store.ReadMeta(info.ID)
			if meta.Profile != info.Profile || meta.HarnessSession != info.HarnessSession {
				t.Fatal("startup failure did not restore the prior conversation")
			}
		})
	}
}
