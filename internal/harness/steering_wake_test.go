package harness

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workeroutput"
)

// doorbellSource models a session doorbell. Wake returns the channel for the
// next signal, and only a Read settles it: the channel stays closed until the
// read that consumes its input, so a run loop that handles another ready event
// first still sees the ring on its next wait.
type doorbellSource struct {
	testSource
	wake     chan struct{}
	rung     bool // the current channel is closed and no read settled it yet
	observed chan struct{}
	reads    int
}

func (s *doorbellSource) Wake() <-chan struct{} {
	s.mu.Lock()
	wake := s.wake
	s.mu.Unlock()
	select {
	case s.observed <- struct{}{}:
	default:
	}
	return wake
}

// ring signals input for the next wait. A second ring before the read settles
// the first one keeps the same closed channel, as the session gate does.
func (s *doorbellSource) ring(message runstore.SteeringMessage) {
	s.mu.Lock()
	s.msgs = append(s.msgs, message)
	if !s.rung {
		close(s.wake)
		s.rung = true
	}
	s.mu.Unlock()
}

func (s *doorbellSource) Read(offset int64, final bool) ([]runstore.SteeringMessage, int64, error) {
	s.mu.Lock()
	s.reads++
	s.mu.Unlock()
	messages, next, err := s.testSource.Read(offset, final)
	s.mu.Lock()
	if s.rung {
		// The read settled the signal, so the next wait gets a fresh doorbell.
		s.wake = make(chan struct{})
		s.rung = false
	}
	s.mu.Unlock()
	return messages, next, err
}

// steeringWait bounds each wait for the run loop's next signal. It only
// expires when the loop stops making progress.
const steeringWait = 30 * time.Second

// awaitDoorbell waits for one signal from the run loop and names what it
// waits for, so a stalled loop fails with the step it never reached.
func awaitDoorbell[T any](t *testing.T, what string, ch <-chan T) T {
	t.Helper()
	timer := time.NewTimer(steeringWait)
	defer timer.Stop()
	select {
	case value := <-ch:
		return value
	case <-timer.C:
		t.Fatalf("%s did not arrive", what)
		var zero T
		return zero
	}
}

func TestNativeSteeringUsesDoorbellWithoutPolling(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	source := &doorbellSource{wake: make(chan struct{}), observed: make(chan struct{}, 1)}
	supervisor := newSteeringSupervisor(SteeringOptions{Source: source, pollEvery: time.Hour}, true)
	tracker := newSessionTracker(func(line string) string {
		_, id := workeroutput.ClaudeStreamResult(line)
		return id
	})

	spec := commandSpec{prompt: "work", streamInput: true, argv: []string{"/bin/sh", "-c", `
printf '%s\n' '{"type":"system","subtype":"init","session_id":"doorbell-session"}'
read -r initial
printf '%s\n' "$initial" | sed 's/"type":"user"/"type":"user","isReplay":true/'
read -r steering
printf '%s\n' "$steering" | sed 's/"type":"user"/"type":"user","isReplay":true/'
printf '%s\n' '{"type":"result","session_id":"doorbell-session","result":"doorbell delivered","is_error":false}'
`}}
	done := make(chan attempt, 1)
	go func() {
		done <- runOnce(ctx, profile.Profile{Harness: profile.HarnessCustom}, t.TempDir(), spec, tracker, nil, supervisor, true, "", "", &workeroutput.Declaration{Dialect: workeroutput.DialectClaudeStreamJSON}, nil, newDeliveryGate(JournalOptions{}), &runState{}, ExecutionOptions{SkipComputer: true})
	}()
	// A worker that stops before it waits on input reports its own error here
	// instead of waiting out the timer.
	select {
	case <-source.observed:
	case got := <-done:
		t.Fatalf("worker ended before it waited on its doorbell: error %v, steered %v, output %s", got.runErr, got.streamSteered, got.stdout)
	case <-time.After(steeringWait):
		t.Fatal("worker never waited on its doorbell")
	}
	source.mu.Lock()
	reads := source.reads
	source.mu.Unlock()
	if reads != 0 {
		t.Fatalf("source read %d times before its doorbell", reads)
	}
	// The doorbell rings while the prompt replay may still be the loop's next
	// ready event. The ring stays closed until its read, so the loop cannot
	// miss it.
	source.ring(runstore.SteeringMessage{ID: "wake-1", Message: "the actual input"})
	got := awaitDoorbell(t, "the steered attempt", done)
	if got.runErr != nil || !got.streamSteered {
		t.Fatalf("doorbell attempt failed: error %v, steered %v, output %s", got.runErr, got.streamSteered, got.stdout)
	}
	source.mu.Lock()
	reads = source.reads
	source.mu.Unlock()
	if reads != 2 {
		t.Fatalf("source reads=%d; want one wake claim and one final seal", reads)
	}
}

// boundarySource models an idle session Read whose fail-open deadline must
// be advanced by the test. A boundary must never enter that read.
type boundarySource struct {
	doorbellSource
	entered  chan struct{}
	deadline chan struct{}
}

func (s *boundarySource) Read(offset int64, final bool) ([]runstore.SteeringMessage, int64, error) {
	if !final {
		select {
		case s.entered <- struct{}{}:
		default:
		}
		<-s.deadline
	}
	return s.doorbellSource.Read(offset, final)
}

func TestIdleDoorbellToolBoundaryDoesNotWaitForInputDeadline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "control")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	control, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	source := &boundarySource{doorbellSource: doorbellSource{wake: make(chan struct{}), observed: make(chan struct{}, 8)}, entered: make(chan struct{}, 2), deadline: make(chan struct{})}
	var once sync.Once
	advance := func() { once.Do(func() { close(source.deadline) }) }
	defer advance()
	supervisor := newSteeringSupervisor(SteeringOptions{Source: source, pollEvery: time.Hour}, true)
	tracker := newSessionTracker(nil)
	tracker.preset("boundary-session")
	spec := commandSpec{argv: []string{"/bin/sh", "-c", `
read -r command < "$BOUNDARY_CONTROL"
printf '%s\n' '{"type":"item.started","item":{"id":"tool-1","type":"command_execution","command":"true","status":"in_progress"}}'
printf '%s\n' '{"type":"item.completed","item":{"id":"tool-1","type":"command_execution","command":"true","status":"completed","exit_code":0}}'
read -r command < "$BOUNDARY_CONTROL"
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"done"}}'
`}}
	done := make(chan attempt, 1)
	go func() {
		done <- runOnce(ctx, profile.Profile{Harness: profile.HarnessCustom, Env: map[string]string{"BOUNDARY_CONTROL": path}}, dir, spec, tracker, nil, supervisor, true, "", "", &workeroutput.Declaration{Dialect: workeroutput.DialectCodexJSONL}, nil, newDeliveryGate(JournalOptions{}), &runState{}, ExecutionOptions{SkipComputer: true})
	}()
	awaitDoorbell(t, "the session observation", source.observed)
	if _, err := control.WriteString("boundary\n"); err != nil {
		t.Fatal(err)
	}
	// The first record wakes session observation. Tool completion wakes the
	// boundary case. The loop must handle both without advancing the deadline.
	for range 2 {
		timer := time.NewTimer(steeringWait)
		select {
		case <-source.observed:
		case <-source.entered:
			timer.Stop()
			t.Fatal("idle tool boundary entered the input deadline wait")
		case <-timer.C:
			t.Fatal("harness loop did not finish the tool boundary")
		}
		timer.Stop()
	}
	source.mu.Lock()
	reads := source.reads
	source.mu.Unlock()
	if reads != 0 {
		t.Fatalf("idle boundary made %d reads", reads)
	}
	advance()
	if _, err := control.WriteString("finish\n"); err != nil {
		t.Fatal(err)
	}
	if got := awaitDoorbell(t, "the boundary worker", done); got.runErr != nil {
		t.Fatalf("boundary worker failed: %v", got.runErr)
	}
}
