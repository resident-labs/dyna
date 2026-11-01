package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness"
	"github.com/resident-labs/dyna/harness/fakeharness"
)

type harnessFake struct {
	mu         sync.Mutex
	started    chan struct{}
	release    chan struct{}
	permit     chan struct{}
	steers     int
	recoveries int
	delivered  map[contract.DeliverySeq]bool
}

func (h *harnessFake) Start(_ context.Context, id contract.TurnID, inputs []contract.TurnInput, started func()) (contract.TurnEnded, error) {
	h.mu.Lock()
	for _, input := range inputs {
		h.delivered[input.Delivery] = true
	}
	h.mu.Unlock()
	close(h.started)
	if h.permit != nil {
		<-h.permit
	}
	started()
	<-h.release
	return contract.TurnEnded{Turn: id, Outcome: contract.TurnOutcome("completed")}, nil
}
func (h *harnessFake) TransferModel(context.Context, contract.TurnID, contract.ModelChoice, int) (harness.ModelTransfer, error) {
	return harness.ModelTransfer{Path: "switched"}, nil
}
func (h *harnessFake) SetPromptSections(context.Context, []contract.ContextSection) error { return nil }
func (h *harnessFake) Continue(context.Context, contract.TurnID, func()) (contract.TurnEnded, error) {
	panic("unexpected continuation")
}
func (h *harnessFake) Recover(id contract.TurnID, _ []contract.TurnInput) (contract.TurnEnded, bool, error) {
	h.mu.Lock()
	h.recoveries++
	h.mu.Unlock()
	return contract.TurnEnded{Turn: id, Outcome: contract.TurnOutcome("completed"), Final: "recovered"}, true, nil
}
func (h *harnessFake) Steer(_ context.Context, input contract.TurnInput) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.steers++
	h.delivered[input.Delivery] = true
	return nil
}
func (h *harnessFake) FollowUp(context.Context, contract.TurnInput) error {
	panic("unexpected follow-up")
}
func (h *harnessFake) Abort(context.Context) error { return nil }
func (h *harnessFake) Delivered(_ context.Context, id contract.DeliverySeq) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.delivered[id], nil
}
func (*harnessFake) Close() error { return nil }

type sinkFake struct {
	mu      sync.Mutex
	acked   map[contract.DeliverySeq]bool
	done    chan contract.TurnEnded
	entered chan struct{}
	release chan struct{}
	calls   int
}

func (s *sinkFake) DeliveryDone(_ context.Context, id contract.DeliverySeq) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acked[id] = true
	return nil
}
func (s *sinkFake) TurnOutput(_ context.Context, result contract.TurnEnded) error {
	s.mu.Lock()
	s.calls++
	first := s.calls == 1
	s.mu.Unlock()
	if first && s.entered != nil {
		close(s.entered)
		<-s.release
	}
	s.done <- result
	return nil
}

type sectionHarness struct {
	fakeharness.Harness
	calls     []string
	sections  []contract.ContextSection
	delivered bool
	setErr    error
}

func (h *sectionHarness) Delivered(context.Context, contract.DeliverySeq) (bool, error) {
	return h.delivered, nil
}
func (h *sectionHarness) Recover(id contract.TurnID, _ []contract.TurnInput) (contract.TurnEnded, bool, error) {
	return contract.TurnEnded{Turn: id}, false, nil
}
func (h *sectionHarness) SetPromptSections(_ context.Context, sections []contract.ContextSection) error {
	h.calls = append(h.calls, "sections")
	h.sections = append([]contract.ContextSection(nil), sections...)
	return h.setErr
}
func (h *sectionHarness) Start(_ context.Context, id contract.TurnID, _ []contract.TurnInput, started func()) (contract.TurnEnded, error) {
	h.calls = append(h.calls, "prompt")
	started()
	return contract.TurnEnded{Turn: id, Outcome: "completed"}, nil
}
func (h *sectionHarness) Continue(_ context.Context, id contract.TurnID, started func()) (contract.TurnEnded, error) {
	h.calls = append(h.calls, "continue")
	started()
	return contract.TurnEnded{Turn: id, Outcome: "completed"}, nil
}

func TestRunnerAppliesSectionsBeforePromptOrRecovery(t *testing.T) {
	for _, tc := range []struct {
		name      string
		delivered bool
		setErr    error
		want      []string
		outcome   contract.TurnOutcome
	}{
		{"prompt", false, nil, []string{"sections", "prompt"}, "completed"},
		{"recovery", true, nil, []string{"sections", "continue"}, "completed"},
		{"failure", false, errors.New("RPC rejected"), []string{"sections"}, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &sectionHarness{delivered: tc.delivered, setErr: tc.setErr}
			sink := &sinkFake{acked: make(map[contract.DeliverySeq]bool), done: make(chan contract.TurnEnded, 1)}
			r := &Runner{Harness: h, Sink: sink}
			sections := []contract.ContextSection{{Key: "rule:a", Text: "first"}, {Key: "goal", Text: "second"}}
			err := r.Handle(context.Background(), contract.BoxMessage{Body: contract.TurnStart{
				Turn: "turn", Inputs: []contract.TurnInput{{Delivery: 1}}, Sections: sections,
			}})
			if err != nil {
				t.Fatal(err)
			}
			got := <-sink.done
			if !reflect.DeepEqual(h.calls, tc.want) || !reflect.DeepEqual(h.sections, sections) || got.Outcome != tc.outcome {
				t.Fatalf("calls=%v sections=%v outcome=%s", h.calls, h.sections, got.Outcome)
			}
		})
	}
}

func TestRunnerReportsRepositorySetupFailure(t *testing.T) {
	sink := &sinkFake{done: make(chan contract.TurnEnded, 1)}
	runner := &Runner{Sink: sink, Setup: func(context.Context) error { return errors.New("cache unavailable") }}
	if err := runner.Handle(context.Background(), contract.BoxMessage{Body: contract.TurnStart{Turn: "turn_a"}}); err != nil {
		t.Fatal(err)
	}
	got := <-sink.done
	if got.Turn != "turn_a" || got.Outcome != "failed" || got.Error != "repository setup: cache unavailable" {
		t.Fatal(got)
	}
}

func TestRunnerDoesNotRestartWhileOutputIsBlocked(t *testing.T) {
	h := &harnessFake{started: make(chan struct{}), release: make(chan struct{}), delivered: make(map[contract.DeliverySeq]bool)}
	sink := &sinkFake{acked: make(map[contract.DeliverySeq]bool), done: make(chan contract.TurnEnded, 2), entered: make(chan struct{}), release: make(chan struct{})}
	runner := &Runner{Harness: h, Sink: sink}
	start := contract.BoxMessage{Body: contract.TurnStart{Turn: "turn_a", Inputs: []contract.TurnInput{{Delivery: 1}}}}
	if err := runner.Handle(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	close(h.release)
	<-sink.entered
	err := runner.Handle(context.Background(), start)
	newTurn := contract.BoxMessage{Body: contract.TurnStart{Turn: "turn_b", Inputs: []contract.TurnInput{{Delivery: 2}}}}
	nextErr := runner.Handle(context.Background(), newTurn)
	h.mu.Lock()
	recovered := h.recoveries
	h.mu.Unlock()
	close(sink.release)
	<-sink.done
	if err != nil || recovered != 0 || nextErr != errTurnSettling {
		t.Fatalf("replay error=%v recoveries=%d next error=%v", err, recovered, nextErr)
	}
}

func TestRunnerRecoversReplayedTurnWithDoneInput(t *testing.T) {
	h := &harnessFake{delivered: map[contract.DeliverySeq]bool{1: true}}
	sink := &sinkFake{acked: make(map[contract.DeliverySeq]bool), done: make(chan contract.TurnEnded, 1)}
	runner := &Runner{Harness: h, Sink: sink}
	if err := runner.Handle(context.Background(), contract.BoxMessage{Body: contract.TurnStart{Turn: "turn_a", Inputs: []contract.TurnInput{{Delivery: 1}}}}); err != nil {
		t.Fatal(err)
	}
	result := <-sink.done
	sink.mu.Lock()
	acked := sink.acked[1]
	sink.mu.Unlock()
	if !acked || result.Final != "recovered" {
		t.Fatalf("acked=%v result=%+v", acked, result)
	}
}

func TestRunnerWaitsForStartBeforeReadingSteer(t *testing.T) {
	h := &harnessFake{started: make(chan struct{}), release: make(chan struct{}), permit: make(chan struct{}), delivered: make(map[contract.DeliverySeq]bool)}
	sink := &sinkFake{acked: make(map[contract.DeliverySeq]bool), done: make(chan contract.TurnEnded, 1)}
	runner := &Runner{Harness: h, Sink: sink}
	result := make(chan error, 1)
	go func() {
		result <- runner.Handle(context.Background(), contract.BoxMessage{Body: contract.TurnStart{Turn: "turn_a", Inputs: []contract.TurnInput{{Delivery: 1}}}})
	}()
	<-h.started
	select {
	case err := <-result:
		t.Fatalf("start returned before Pi accepted: %v", err)
	default:
	}
	close(h.permit)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	close(h.release)
	<-sink.done
}

func TestRunnerSkipsReplayedSteer(t *testing.T) {
	h := &harnessFake{started: make(chan struct{}), release: make(chan struct{}), delivered: make(map[contract.DeliverySeq]bool)}
	sink := &sinkFake{acked: make(map[contract.DeliverySeq]bool), done: make(chan contract.TurnEnded, 1)}
	runner := &Runner{Harness: h, Sink: sink}
	ctx := context.Background()
	if err := runner.Handle(ctx, contract.BoxMessage{Body: contract.TurnStart{Turn: "turn_a", Inputs: []contract.TurnInput{{Delivery: 1, Text: "start"}}}}); err != nil {
		t.Fatal(err)
	}
	<-h.started
	steer := contract.BoxMessage{Body: contract.TurnSteer{Input: contract.TurnInput{Delivery: 2, Text: "update"}, Queue: contract.QueueSteer}}
	if err := runner.Handle(ctx, steer); err != nil {
		t.Fatal(err)
	}
	if err := runner.Handle(ctx, steer); err != nil {
		t.Fatal(err)
	}
	close(h.release)
	<-sink.done
	h.mu.Lock()
	count := h.steers
	h.mu.Unlock()
	sink.mu.Lock()
	acked := sink.acked[1] && sink.acked[2]
	sink.mu.Unlock()
	if count != 1 || !acked {
		t.Fatalf("steers=%d acked=%v", count, acked)
	}
}

func turnStart(i int, model contract.ModelChoice, compactTokens int) contract.BoxMessage {
	return contract.BoxMessage{Body: contract.TurnStart{Turn: contract.TurnID(fmt.Sprint("turn_", i)), Inputs: []contract.TurnInput{{Delivery: contract.DeliverySeq(i + 1)}}, Model: model, CompactTokens: compactTokens}}
}

var (
	modelA = contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: "a", Thinking: contract.ThinkingLow}
	modelB = contract.ModelChoice{Provider: contract.ModelProviderOpenAI, ID: "b", Thinking: contract.ThinkingLow}
)

// The first turn of a Pi process always transfers: box.hello started Pi on this model, but the session file may hold
// a conversation from another one, or more than this model takes.
func TestRunnerTransfersOnFirstTurnAndOnEachChange(t *testing.T) {
	completed := fakeharness.Turn{Result: contract.TurnEnded{Outcome: "completed"}}
	h := &fakeharness.Harness{Turns: []fakeharness.Turn{completed, completed, completed, completed}}
	sink := &sinkFake{acked: make(map[contract.DeliverySeq]bool), done: make(chan contract.TurnEnded, 1)}
	runner := &Runner{Harness: h, Sink: sink}
	turns := []struct {
		model  contract.ModelChoice
		tokens int
	}{{modelA, 0}, {modelB, 0}, {modelB, 0}, {modelB, 150000}}
	for i, turn := range turns {
		if err := runner.Handle(context.Background(), turnStart(i, turn.model, turn.tokens)); err != nil {
			t.Fatal(err)
		}
		<-sink.done
	}
	if want := []contract.ModelChoice{modelA, modelB, modelB}; !reflect.DeepEqual(h.Models, want) {
		t.Fatalf("transfers: %+v", h.Models)
	}
	if want := []int{0, 0, 150000}; !reflect.DeepEqual(h.Triggers, want) {
		t.Fatalf("triggers: %+v", h.Triggers)
	}
}

func TestRunnerOnlyARefusedModelFailsTheTurn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reply   harness.ModelTransfer
		err     error
		outcome contract.TurnOutcome
		cause   contract.FailureCause
	}{
		{"compacted", harness.ModelTransfer{Path: "compacted"}, nil, "completed", ""},
		{"boundary", harness.ModelTransfer{Path: "boundary", Reason: "quota exhausted"}, nil, "completed", ""},
		{"refused", harness.ModelTransfer{}, fmt.Errorf("Model not found: %w", harness.ErrRefused), "failed", contract.CauseSetModelFailed},
		{"pi stopped", harness.ModelTransfer{}, errors.New("pi RPC stream closed"), "failed", contract.CauseHarnessFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &fakeharness.Harness{Turns: []fakeharness.Turn{{Result: contract.TurnEnded{Outcome: "completed"}}, {Result: contract.TurnEnded{Outcome: "completed"}}}}
			h.Transfer = func(context.Context) (harness.ModelTransfer, error) { return tc.reply, tc.err }
			sink := &sinkFake{acked: make(map[contract.DeliverySeq]bool), done: make(chan contract.TurnEnded, 1)}
			runner := &Runner{Harness: h, Sink: sink}
			if err := runner.Handle(context.Background(), turnStart(0, modelB, 0)); err != nil {
				t.Fatal(err)
			}
			if got := <-sink.done; got.Outcome != tc.outcome || got.Cause != tc.cause {
				t.Fatalf("turn: %+v", got)
			}
			// A transfer that failed stays pending: the next turn tries again.
			if err := runner.Handle(context.Background(), turnStart(1, modelB, 0)); err != nil {
				t.Fatal(err)
			}
			<-sink.done
			if want := map[bool]int{true: 2, false: 1}[tc.err != nil]; len(h.Models) != want {
				t.Fatalf("transfers: %d, want %d", len(h.Models), want)
			}
		})
	}
}

// transferHarness holds a transfer open until Abort or release, and records what reached Pi in order.
type transferHarness struct {
	fakeharness.Harness
	mu      sync.Mutex
	calls   []string
	entered chan struct{}
	release chan harness.ModelTransfer
	aborted chan struct{}
}

func newTransferHarness() *transferHarness {
	h := &transferHarness{entered: make(chan struct{}), release: make(chan harness.ModelTransfer, 1), aborted: make(chan struct{}, 1)}
	h.Harness.Transfer = func(ctx context.Context) (harness.ModelTransfer, error) {
		close(h.entered)
		select {
		case reply := <-h.release:
			return reply, nil
		case <-h.aborted:
			return harness.ModelTransfer{Path: harness.TransferAborted}, nil
		}
	}
	return h
}

func (h *transferHarness) record(call string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, call)
}
func (h *transferHarness) Abort(context.Context) error {
	h.record("abort")
	h.aborted <- struct{}{}
	return nil
}
func (h *transferHarness) Steer(_ context.Context, input contract.TurnInput) error {
	h.record(input.Text)
	return nil
}
func (h *transferHarness) Delivered(context.Context, contract.DeliverySeq) (bool, error) {
	return false, nil
}
func (h *transferHarness) Start(_ context.Context, id contract.TurnID, _ []contract.TurnInput, started func()) (contract.TurnEnded, error) {
	h.record("prompt")
	started()
	return contract.TurnEnded{Turn: id, Outcome: "completed"}, nil
}

// A compaction takes minutes, so the channel must keep reading: a stop has to reach it, and a steer must not reach Pi
// before the prompt does.
func TestRunnerReadsTheChannelDuringATransfer(t *testing.T) {
	h := newTransferHarness()
	sink := &sinkFake{acked: make(map[contract.DeliverySeq]bool), done: make(chan contract.TurnEnded, 1)}
	runner := &Runner{Harness: h, Sink: sink}
	ctx := context.Background()
	if err := runner.Handle(ctx, turnStart(0, modelB, 0)); err != nil {
		t.Fatal(err)
	}
	<-h.entered
	for _, delivery := range []contract.DeliverySeq{2, 3, 4} {
		steer := contract.TurnSteer{Queue: contract.QueueSteer, Input: contract.TurnInput{Delivery: delivery, Text: fmt.Sprint("steer ", delivery)}}
		if err := runner.Handle(ctx, contract.BoxMessage{Body: steer}); err != nil {
			t.Fatal(err)
		}
	}
	h.mu.Lock()
	early := len(h.calls)
	h.mu.Unlock()
	if early != 0 {
		t.Fatalf("Pi saw %d calls during the transfer", early)
	}
	h.release <- harness.ModelTransfer{Path: "compacted"}
	<-sink.done
	if want := []string{"prompt", "steer 2", "steer 3", "steer 4"}; !reflect.DeepEqual(h.calls, want) {
		t.Fatalf("calls: %v", h.calls)
	}
}

func TestRunnerStopCancelsATransfer(t *testing.T) {
	h := newTransferHarness()
	sink := &sinkFake{acked: make(map[contract.DeliverySeq]bool), done: make(chan contract.TurnEnded, 1)}
	runner := &Runner{Harness: h, Sink: sink}
	ctx := context.Background()
	if err := runner.Handle(ctx, turnStart(0, modelB, 0)); err != nil {
		t.Fatal(err)
	}
	<-h.entered
	if err := runner.Handle(ctx, contract.BoxMessage{Body: contract.TurnSteer{Queue: contract.QueueSteer, Input: contract.TurnInput{Delivery: 2, Text: "steer"}}}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Handle(ctx, contract.BoxMessage{Body: contract.TurnStop{Turn: "turn_0"}}); err != nil {
		t.Fatal(err)
	}
	got := <-sink.done
	if got.Outcome != "stopped" || got.Cause != contract.CauseNotStarted {
		t.Fatalf("turn: %+v", got)
	}
	if want := []string{"abort"}; !reflect.DeepEqual(h.calls, want) {
		t.Fatalf("Pi saw inputs of a stopped turn: %v", h.calls)
	}
	// Pi stayed on its model, so the next turn transfers again.
	h.entered = make(chan struct{})
	h.release <- harness.ModelTransfer{Path: "switched"}
	if err := runner.Handle(ctx, turnStart(1, modelB, 0)); err != nil {
		t.Fatal(err)
	}
	if got := <-sink.done; got.Outcome != "completed" {
		t.Fatalf("turn: %+v", got)
	}
	if len(h.Models) != 2 {
		t.Fatalf("transfers: %d", len(h.Models))
	}
}

// A stop can land after Pi finished the transfer, when Abort finds nothing to cancel.
func TestRunnerStopAfterTheTransferStillStopsTheTurn(t *testing.T) {
	sections := make(chan struct{})
	entered := make(chan struct{})
	h := &stopLateHarness{transferHarness: newTransferHarness(), sections: sections, entered: entered}
	h.aborted = make(chan struct{}, 8)
	sink := &sinkFake{acked: make(map[contract.DeliverySeq]bool), done: make(chan contract.TurnEnded, 1)}
	runner := &Runner{Harness: h, Sink: sink}
	ctx := context.Background()
	h.release <- harness.ModelTransfer{Path: "switched"}
	if err := runner.Handle(ctx, turnStart(0, modelB, 0)); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := runner.Handle(ctx, contract.BoxMessage{Body: contract.TurnStop{Turn: "turn_0"}}); err != nil {
		t.Fatal(err)
	}
	close(sections)
	if got := <-sink.done; got.Outcome != "stopped" || got.Cause != contract.CauseNotStarted {
		t.Fatalf("turn: %+v", got)
	}
	if want := []string{"abort"}; !reflect.DeepEqual(h.calls, want) {
		t.Fatalf("calls: %v", h.calls)
	}
}

type stopLateHarness struct {
	*transferHarness
	sections chan struct{}
	entered  chan struct{}
}

func (h *stopLateHarness) SetPromptSections(context.Context, []contract.ContextSection) error {
	close(h.entered)
	<-h.sections
	return nil
}
