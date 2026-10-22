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

type doorbellSource struct {
	testSource
	wake     chan struct{}
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

func (s *doorbellSource) Read(offset int64, final bool) ([]runstore.SteeringMessage, int64, error) {
	s.mu.Lock()
	s.reads++
	s.mu.Unlock()
	return s.testSource.Read(offset, final)
}

func awaitDoorbell[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case value := <-ch:
		return value
	case <-timer.C:
		t.Fatal("steering event did not arrive")
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
	awaitDoorbell(t, source.observed)
	source.mu.Lock()
	reads := source.reads
	source.msgs = append(source.msgs, runstore.SteeringMessage{ID: "wake-1", Message: "the actual input"})
	close(source.wake)
	source.wake = make(chan struct{})
	source.mu.Unlock()
	if reads != 0 {
		t.Fatalf("source read %d times before its doorbell", reads)
	}
	got := awaitDoorbell(t, done)
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
	awaitDoorbell(t, source.observed)
	if _, err := control.WriteString("boundary\n"); err != nil {
		t.Fatal(err)
	}
	// The first record wakes session observation. Tool completion wakes the
	// boundary case. The loop must handle both without advancing the deadline.
	for range 2 {
		timer := time.NewTimer(30 * time.Second)
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
	if got := awaitDoorbell(t, done); got.runErr != nil {
		t.Fatalf("boundary worker failed: %v", got.runErr)
	}
}
