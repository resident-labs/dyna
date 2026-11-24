package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/agent"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/pig"
	"github.com/resident-labs/dyna/harness/pig/pigtest"
)

func TestSessionClearAccessAndRefusals(t *testing.T) {
	s, room, worker, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	tree, err := s.store.RoomTree(ctx, room)
	if err != nil {
		t.Fatal(err)
	}
	root := tree.Root()

	// Agents cannot call session.clear (owner only).
	for _, caller := range []contract.Principal{sessionCaller(root), sessionCaller(worker)} {
		if _, problem := s.SessionClear(ctx, caller, contract.SessionClearRequest{Session: worker}); problem == nil || problem.Code != contract.ErrForbidden {
			t.Fatalf("caller %+v: got %+v, want forbidden", caller, problem)
		}
	}

	// Unknown session returns not_found.
	if _, problem := s.SessionClear(ctx, owner, contract.SessionClearRequest{Session: "ses_missing"}); problem == nil || problem.Code != contract.ErrNotFound {
		t.Fatalf("missing session: %+v", problem)
	}

	// Running turn refuses with "stop the turn first".
	channel, problem := testChannel(t, s, ctx, boxCaller(worker))
	if problem != nil {
		t.Fatal(problem)
	}
	frame := (<-channel).Body.(contract.TurnStart)
	if _, problem := s.SessionClear(ctx, owner, contract.SessionClearRequest{Session: worker}); problem == nil || problem.Code != contract.ErrConflict || problem.Message != "stop the turn first" {
		t.Fatalf("while turn running: %+v", problem)
	}

	// Finish the turn so the session is idle, then clear works for both worker and root.
	if _, problem := s.TurnOutput(ctx, boxCaller(worker), contract.TurnOutputRequest{Turn: frame.Turn, Outcome: contract.TurnCompleted, Final: "done"}); problem != nil {
		t.Fatal(problem)
	}
	for _, target := range []contract.SessionID{worker, root} {
		if _, problem := s.SessionClear(ctx, owner, contract.SessionClearRequest{Session: target}); problem != nil {
			t.Fatalf("clear %s: %+v", target, problem)
		}
	}

	// Closing session is refused.
	if _, problem := s.SessionClose(ctx, owner, contract.SessionCloseRequest{Session: worker}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := s.SessionClear(ctx, owner, contract.SessionClearRequest{Session: worker}); problem == nil || problem.Code != contract.ErrConflict {
		t.Fatalf("closing session: %+v", problem)
	}

	// session.cleared events were recorded in the room feed.
	listed, problem := s.EventsList(ctx, owner, contract.EventsListRequest{Room: room, Types: []contract.EventType{"session.cleared"}, Limit: 20})
	if problem != nil {
		t.Fatal(problem)
	}
	if len(listed.Events) != 2 {
		t.Fatalf("expected 2 session.cleared events, got %d", len(listed.Events))
	}
	got := map[contract.SessionID]bool{}
	for _, ev := range listed.Events {
		got[ev.Body.(contract.SessionCleared).Session] = true
	}
	if !got[worker] || !got[root] {
		t.Fatalf("unexpected cleared sessions: %+v", got)
	}
}

type clearTestSink struct {
	s      *Service
	caller contract.Principal
	tr     *agent.Transcript
	files  func() []string
	done   chan contract.TurnEnded
}

func (k *clearTestSink) DeliveryDone(ctx context.Context, seq contract.DeliverySeq) error {
	_, problem := k.s.DeliveryDone(ctx, k.caller, contract.DeliveryDoneRequest{Delivery: seq})
	if problem != nil {
		return errors.New(problem.Message)
	}
	return nil
}

func (k *clearTestSink) TurnOutput(ctx context.Context, result contract.TurnEnded) error {
	_, problem := k.s.TurnOutput(ctx, k.caller, contract.TurnOutputRequest{
		Turn:    result.Turn,
		Outcome: result.Outcome,
		Final:   result.Final,
		Model:   result.Usage.Model,
		Usage:   result.Usage,
		Error:   result.Error,
		Cause:   result.Cause,
	})
	if problem != nil {
		return errors.New(problem.Message)
	}
	for _, f := range k.files() {
		_ = k.tr.Sync(ctx, f)
	}
	k.done <- result
	return nil
}

func (k *clearTestSink) Compacted(context.Context, contract.BoxCompactedRequest) error { return nil }

type clearTranscriptSink struct {
	s      *Service
	caller contract.Principal
}

func (k clearTranscriptSink) TranscriptAppend(ctx context.Context, req contract.TranscriptAppendRequest) (contract.TranscriptAppendResponse, error) {
	resp, problem := k.s.TranscriptAppend(ctx, k.caller, req)
	if problem != nil {
		return resp, errors.New(problem.Message)
	}
	return resp, nil
}

// End-to-end live-style test: real Engine + real Store + real Box Runner + real PiG harness + fake model server.
// Verifies that after session.clear:
// - the next turn's model request sees no prior conversation history;
// - instructions and context sections (goal, etc.) stay intact;
// - old transcript file stays on disk and TranscriptRead returns both turns with clearedAt divider metadata;
// - BoxHello reports Fresh=true while a clear is pending (e.g., if the box restarts before the next turn).
func TestSessionClearEndToEndFreshConversation(t *testing.T) {
	s, room, worker, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	s.profiles = spacingProfiles{model: contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: "test-model", Thinking: contract.ThinkingLow}}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	caller := boxCaller(worker)

	model := pigtest.NewModel(
		[]pigtest.Block{{Text: "Noted secret giraffe."}},
		[]pigtest.Block{{Text: "I do not know any secret."}},
	)
	agentDir := pigtest.AgentDir(t, model, "")
	sessionDir := filepath.Join(agentDir, "sessions")

	hello, problem := s.BoxHello(ctx, caller, contract.BoxHelloRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	if hello.Fresh {
		t.Fatal("initial BoxHello should not have Fresh=true")
	}
	h, err := pig.Open(ctx, pig.Config{
		Dir:          t.TempDir(),
		AgentDir:     agentDir,
		SessionDir:   sessionDir,
		Model:        contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: "test-model", Thinking: contract.ThinkingLow},
		Instructions: hello.Instructions,
		New:          hello.Fresh,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	tr := agent.NewTranscript(clearTranscriptSink{s: s, caller: caller}, sessionDir)
	done := make(chan contract.TurnEnded, 4)
	sink := &clearTestSink{s: s, caller: caller, tr: tr, files: func() []string { return []string{h.SessionFile()} }, done: done}
	runner := &agent.Runner{Harness: h, Sink: sink}

	channel, problem := testChannel(t, s, ctx, caller)
	if problem != nil {
		t.Fatal(problem)
	}

	runNextTurn := func() contract.TurnStart {
		t.Helper()
		var start contract.TurnStart
		select {
		case msg := <-channel:
			var ok bool
			start, ok = msg.Body.(contract.TurnStart)
			if !ok {
				t.Fatalf("expected TurnStart, got %T", msg.Body)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for TurnStart")
		}
		if err := runner.Handle(ctx, contract.BoxMessage{Body: start}); err != nil {
			t.Fatal(err)
		}
		select {
		case res := <-done:
			if res.Outcome != contract.TurnCompleted {
				t.Fatalf("turn failed: %+v", res)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for turn output")
		}
		return start
	}

	// Turn 1: initial brief from setup.
	t1 := runNextTurn()
	if t1.Fresh {
		t.Fatal("turn 1 should not be fresh")
	}
	oldFile := h.SessionFile()

	// Clear the session while idle.
	if _, problem := s.SessionClear(ctx, owner, contract.SessionClearRequest{Session: worker}); problem != nil {
		t.Fatal(problem)
	}

	// If the box reconnects before the next turn, BoxHello reports Fresh=true.
	helloAfterClear, problem := s.BoxHello(ctx, caller, contract.BoxHelloRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	if !helloAfterClear.Fresh {
		t.Fatal("BoxHello should report Fresh=true while a clear is pending")
	}

	// TranscriptRead reports pending clearedAt before the next turn starts.
	beforeTurn2, problem := s.TranscriptRead(ctx, owner, contract.TranscriptReadRequest{Session: worker})
	if problem != nil {
		t.Fatal(problem)
	}
	if beforeTurn2.ClearedAt == nil {
		t.Fatal("TranscriptRead should report pending ClearedAt before next turn")
	}

	// Owner sends a new DM message to trigger Turn 2.
	if _, problem := s.Post(ctx, owner, contract.PostRequest{
		Room: room,
		Home: contract.Conversation{Kind: contract.ConversationDM, Session: worker},
		Text: "What animal did I mention earlier?",
	}); problem != nil {
		t.Fatal(problem)
	}

	t2 := runNextTurn()
	if !t2.Fresh {
		t.Fatal("turn 2 should have Fresh=true")
	}
	newFile := h.SessionFile()
	if newFile == oldFile {
		t.Fatalf("turn 2 reused old session file %s", oldFile)
	}
	if _, err := os.Stat(oldFile); err != nil {
		t.Fatalf("old session file was removed: %v", err)
	}

	// Verify model requests: Turn 2 had no prior conversation messages, and kept system prompt + goal section.
	reqs := model.Requests()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 model requests, got %d", len(reqs))
	}
	if len(reqs[1].Messages) != 1 {
		t.Fatalf("turn 2 should have 1 user message, got %d: %s", len(reqs[1].Messages), reqs[1].Messages)
	}
	if strings.Contains(string(reqs[1].Messages[0]), "giraffe") || strings.Contains(string(reqs[1].Messages[0]), "start") {
		t.Fatalf("turn 2 leaked prior conversation: %s", reqs[1].Messages[0])
	}
	if !strings.Contains(string(reqs[1].System), "dyna_goal") || !strings.Contains(string(reqs[1].System), "goal") {
		t.Fatalf("turn 2 missing goal context section: %s", reqs[1].System)
	}

	// Verify TranscriptRead keeps old turn and marks turn 2 with ClearedAt, clearing the pending flag.
	afterTurn2, problem := s.TranscriptRead(ctx, owner, contract.TranscriptReadRequest{Session: worker})
	if problem != nil {
		t.Fatal(problem)
	}
	if afterTurn2.ClearedAt != nil {
		t.Fatalf("pending ClearedAt should be cleared after turn 2, got %v", afterTurn2.ClearedAt)
	}
	if len(afterTurn2.Turns) != 2 {
		t.Fatalf("expected 2 turns in transcript, got %d", len(afterTurn2.Turns))
	}
	// Newest first: Turns[0] is turn 2 (with ClearedAt), Turns[1] is turn 1 (without ClearedAt).
	if afterTurn2.Turns[0].ID != t2.Turn || afterTurn2.Turns[0].ClearedAt == nil {
		t.Fatalf("turn 2 missing ClearedAt: %+v", afterTurn2.Turns[0])
	}
	if afterTurn2.Turns[1].ID != t1.Turn || afterTurn2.Turns[1].ClearedAt != nil {
		t.Fatalf("turn 1 should not have ClearedAt: %+v", afterTurn2.Turns[1])
	}

	// BoxHello after turn 2 no longer reports Fresh=true.
	helloAfterTurn2, problem := s.BoxHello(ctx, caller, contract.BoxHelloRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	if helloAfterTurn2.Fresh {
		t.Fatal("BoxHello should not report Fresh=true after turn 2 consumed the clear")
	}

	// Session's goal is still active.
	goals, problem := s.GoalList(ctx, owner, contract.GoalListRequest{Room: room, Session: worker})
	if problem != nil || len(goals.Goals) != 1 {
		t.Fatalf("session goal was not preserved: %+v (%v)", goals, problem)
	}
}

// Double clear before a turn stays pending; if the first turn after clear is stopped before starting
// (CauseNotStarted) or fails before writing a fresh turn, the clear is restored so the next turn still starts fresh.
func TestSessionClearPreservedAcrossDoubleClearAndPreStartStop(t *testing.T) {
	s, room, worker, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	caller := boxCaller(worker)

	channel, problem := testChannel(t, s, ctx, caller)
	if problem != nil {
		t.Fatal(problem)
	}
	// Finish initial turn 1.
	t1 := (<-channel).Body.(contract.TurnStart)
	if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: t1.Turn, Outcome: contract.TurnCompleted, Final: "done"}); problem != nil {
		t.Fatal(problem)
	}

	// Clearing twice while idle succeeds and keeps a single pending clear.
	if _, problem := s.SessionClear(ctx, owner, contract.SessionClearRequest{Session: worker}); problem != nil {
		t.Fatalf("first clear: %+v", problem)
	}
	if _, problem := s.SessionClear(ctx, owner, contract.SessionClearRequest{Session: worker}); problem != nil {
		t.Fatalf("second clear: %+v", problem)
	}

	// Post triggers turn 2 (Fresh=true), which is stopped before starting (CauseNotStarted).
	if _, problem := s.Post(ctx, owner, contract.PostRequest{
		Room: room,
		Home: contract.Conversation{Kind: contract.ConversationDM, Session: worker},
		Text: "first attempt",
	}); problem != nil {
		t.Fatal(problem)
	}
	t2 := (<-channel).Body.(contract.TurnStart)
	if !t2.Fresh {
		t.Fatal("turn 2 should have Fresh=true")
	}
	if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{
		Turn:    t2.Turn,
		Outcome: contract.TurnStopped,
		Cause:   contract.CauseNotStarted,
	}); problem != nil {
		t.Fatal(problem)
	}

	// Pending clear must be restored on the session and removed from the unstarted turn.
	hello, problem := s.BoxHello(ctx, caller, contract.BoxHelloRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	if !hello.Fresh {
		t.Fatal("BoxHello should still report Fresh=true after pre-start stop")
	}
	tr, problem := s.TranscriptRead(ctx, owner, contract.TranscriptReadRequest{Session: worker})
	if problem != nil {
		t.Fatal(problem)
	}
	if tr.ClearedAt == nil {
		t.Fatal("TranscriptRead should still report pending ClearedAt after pre-start stop")
	}
	for _, turn := range tr.Turns {
		if turn.ID == t2.Turn && turn.ClearedAt != nil {
			t.Fatalf("unstarted turn %s should not carry ClearedAt", t2.Turn)
		}
	}

	// Next turn after a new message still receives Fresh=true.
	if _, problem := s.Post(ctx, owner, contract.PostRequest{
		Room: room,
		Home: contract.Conversation{Kind: contract.ConversationDM, Session: worker},
		Text: "second attempt",
	}); problem != nil {
		t.Fatal(problem)
	}
	t3 := (<-channel).Body.(contract.TurnStart)
	if !t3.Fresh {
		t.Fatal("turn 3 should still have Fresh=true after turn 2 stopped before starting")
	}
	if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: t3.Turn, Outcome: contract.TurnCompleted, Final: "fresh"}); problem != nil {
		t.Fatal(problem)
	}
}
