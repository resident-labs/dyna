package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness"
	"github.com/resident-labs/dyna/harness/fakeharness"
)

// compactHarness holds a compaction open until the test releases it, and notes what Pi was asked in order.
type compactHarness struct {
	fakeharness.Harness
	entered chan int64
	release chan harness.Compaction
	fail    error
	aborted chan struct{}
	calls   chan string
}

func newCompactHarness() *compactHarness {
	return &compactHarness{entered: make(chan int64, 1), release: make(chan harness.Compaction, 1), aborted: make(chan struct{}, 1), calls: make(chan string, 8)}
}

func (h *compactHarness) Compact(ctx context.Context, over int64) (harness.Compaction, error) {
	h.entered <- over
	select {
	case result := <-h.release:
		return result, h.fail
	case <-h.aborted:
		return harness.Compaction{}, errors.New("Compaction cancelled")
	case <-ctx.Done():
		return harness.Compaction{}, ctx.Err()
	}
}

func (h *compactHarness) Abort(context.Context) error {
	h.aborted <- struct{}{}
	return nil
}
func (*compactHarness) Delivered(context.Context, contract.DeliverySeq) (bool, error) {
	return false, nil
}
func (h *compactHarness) SetPromptSections(context.Context, []contract.ContextSection) error {
	h.calls <- "sections"
	return nil
}
func (h *compactHarness) Start(_ context.Context, id contract.TurnID, _ []contract.TurnInput, started func()) (contract.TurnEnded, error) {
	h.calls <- "prompt"
	started()
	return contract.TurnEnded{Turn: id, Outcome: "completed"}, nil
}

func waitCompacted(t *testing.T, sink *sinkFake, want int) []contract.BoxCompactedRequest {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		sink.mu.Lock()
		answers := append([]contract.BoxCompactedRequest(nil), sink.compacted...)
		sink.mu.Unlock()
		if len(answers) >= want {
			return answers
		}
		select {
		case <-deadline:
			t.Fatalf("%d answers, want %d", len(answers), want)
		case <-time.After(time.Millisecond):
		}
	}
}

func compactRunner() (*compactHarness, *sinkFake, *Runner) {
	h := newCompactHarness()
	sink := &sinkFake{acked: make(map[contract.DeliverySeq]bool), done: make(chan contract.TurnEnded, 1)}
	return h, sink, &Runner{Harness: h, Sink: sink}
}

func compactAsk() contract.BoxMessage {
	return contract.BoxMessage{Body: contract.Compact{Turn: "turn_last", OverTokens: 100000}}
}

func TestRunnerAnswersACompactionAndAsksPiForTheLimit(t *testing.T) {
	h, sink, runner := compactRunner()
	ctx := context.Background()
	if err := runner.Handle(ctx, compactAsk()); err != nil {
		t.Fatal(err)
	}
	if over := <-h.entered; over != 100000 {
		t.Fatalf("limit %d", over)
	}
	// An engine that restarted asks again, and the running compaction answers both.
	if err := runner.Handle(ctx, compactAsk()); err != nil {
		t.Fatal(err)
	}
	h.release <- harness.Compaction{Compacted: true, TokensBefore: 150000, TokensAfter: 9000, Usage: contract.Usage{InputTokens: 140000}}
	answers := waitCompacted(t, sink, 1)
	got := answers[0]
	if len(answers) != 1 || got.Turn != "turn_last" || !got.Compacted || got.TokensBefore != 150000 || got.TokensAfter != 9000 || got.Usage.InputTokens != 140000 || got.Error != "" {
		t.Fatalf("answers %+v", answers)
	}
}

func TestRunnerReportsAFailedCompaction(t *testing.T) {
	h, sink, runner := compactRunner()
	h.fail = errors.New("the model is down")
	if err := runner.Handle(context.Background(), compactAsk()); err != nil {
		t.Fatal(err)
	}
	<-h.entered
	h.release <- harness.Compaction{}
	if answers := waitCompacted(t, sink, 1); answers[0].Compacted || answers[0].Error != "the model is down" {
		t.Fatalf("answers %+v", answers)
	}
}

func TestRunnerLeavesARunningTurnAlone(t *testing.T) {
	_, sink, runner := compactRunner()
	inner := &harnessFake{started: make(chan struct{}), release: make(chan struct{}), delivered: map[contract.DeliverySeq]bool{}}
	runner.Harness = inner
	ctx := context.Background()
	if err := runner.Handle(ctx, contract.BoxMessage{Body: contract.TurnStart{Turn: "turn_a", Inputs: []contract.TurnInput{{Delivery: 1}}}}); err != nil {
		t.Fatal(err)
	}
	<-inner.started
	if err := runner.Handle(ctx, compactAsk()); err != nil {
		t.Fatal(err)
	}
	if answers := waitCompacted(t, sink, 1); answers[0].Compacted || answers[0].Error != "" {
		t.Fatalf("answers %+v", answers)
	}
	close(inner.release)
	<-sink.done
}

func TestAWakeDuringACompactionWaitsThenRunsItsTurn(t *testing.T) {
	h, sink, runner := compactRunner()
	ctx := context.Background()
	if err := runner.Handle(ctx, compactAsk()); err != nil {
		t.Fatal(err)
	}
	<-h.entered
	start := contract.BoxMessage{Body: contract.TurnStart{Turn: "turn_a", Inputs: []contract.TurnInput{{Delivery: 1}}}}
	if err := runner.Handle(ctx, start); err != nil {
		t.Fatal(err)
	}
	// The channel keeps reading, and a steer waits with the turn.
	steer := contract.TurnSteer{Queue: contract.QueueSteer, Input: contract.TurnInput{Delivery: 2}}
	if err := runner.Handle(ctx, contract.BoxMessage{Body: steer}); err != nil {
		t.Fatal(err)
	}
	select {
	case call := <-h.calls:
		t.Fatalf("Pi was sent %q during its compaction", call)
	case <-time.After(20 * time.Millisecond):
	}
	h.release <- harness.Compaction{Compacted: true}
	if first, second := <-h.calls, <-h.calls; first != "sections" || second != "prompt" {
		t.Fatalf("calls %s, %s", first, second)
	}
	if ended := <-sink.done; ended.Outcome != "completed" {
		t.Fatalf("ended %+v", ended)
	}
	if len(h.Inputs) != 1 || h.Inputs[0].TurnInput.Delivery != 2 {
		t.Fatalf("the steer was lost: %+v", h.Inputs)
	}
}

func TestAStopDuringThatWaitCancelsTheCompactionAndReturnsTheInputs(t *testing.T) {
	h, sink, runner := compactRunner()
	ctx := context.Background()
	if err := runner.Handle(ctx, compactAsk()); err != nil {
		t.Fatal(err)
	}
	<-h.entered
	if err := runner.Handle(ctx, contract.BoxMessage{Body: contract.TurnStart{Turn: "turn_a", Inputs: []contract.TurnInput{{Delivery: 1}}}}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Handle(ctx, contract.BoxMessage{Body: contract.TurnStop{Turn: "turn_a"}}); err != nil {
		t.Fatal(err)
	}
	ended := <-sink.done
	if ended.Outcome != "stopped" || ended.Cause != contract.CauseNotStarted {
		t.Fatalf("ended %+v", ended)
	}
	if answers := waitCompacted(t, sink, 1); answers[0].Error == "" {
		t.Fatalf("answers %+v", answers)
	}
}

func TestACrewNeverCompacts(t *testing.T) {
	if got, err := (&Crew{}).Compact(context.Background(), 1); err != nil || got.Compacted {
		t.Fatalf("%+v %v", got, err)
	}
}
