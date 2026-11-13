package agent

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestPauseGatesWorkersAndNewCalls(t *testing.T) {
	c := &Crew{workflow: contract.BoxWorkflow{Paused: true, Version: 1}, pauseChanged: make(chan struct{})}
	results := make(chan string, 3)
	for _, key := range []string{"a", "b", ""} {
		go func(key string) {
			if err := c.waitUnpaused(t.Context(), key); err == nil {
				results <- key
			}
		}(key)
	}
	select {
	case key := <-results:
		t.Fatalf("paused call %q escaped", key)
	default:
	}
	c.Reload(contract.BoxWorkflow{Version: 1, PausedSteps: []string{"a"}})
	for range 2 {
		select {
		case key := <-results:
			if key == "a" {
				t.Fatal("worker a escaped its own hold")
			}
		case <-time.After(time.Second):
			t.Fatal("run unpause did not release worker b and new call")
		}
	}
	c.Reload(contract.BoxWorkflow{Version: 1})
	select {
	case key := <-results:
		if key != "a" {
			t.Fatalf("released %q", key)
		}
	case <-time.After(time.Second):
		t.Fatal("step unpause did not release worker")
	}
}

func TestPauseGateHonorsCancellation(t *testing.T) {
	c := &Crew{workflow: contract.BoxWorkflow{Paused: true}, pauseChanged: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.waitUnpaused(ctx, "a"); err != context.Canceled {
		t.Fatalf("gate: %v", err)
	}
}

func TestPausedWorkerDoesNotSpendItsTimeout(t *testing.T) {
	h := newCrewHarness(t)
	h.timeout = 1
	entered, release := make(chan struct{}), make(chan struct{})
	h.hold = func(ctx context.Context, _ string) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	crew, ended := h.start(crewMeta + "return await agent('held')")
	<-entered
	crew.mu.Lock()
	workflow := crew.workflow
	crew.mu.Unlock()
	workflow.Paused = true
	crew.Reload(workflow)
	close(release)
	timer := time.NewTimer(1200 * time.Millisecond)
	defer timer.Stop()
	select {
	case got := <-ended:
		t.Fatalf("held worker ended before release: %+v", got)
	case <-timer.C:
	}
	workflow.Paused = false
	crew.Reload(workflow)
	if got := <-ended; got.Outcome != contract.TurnCompleted {
		t.Fatalf("released worker: %+v", got)
	}
}

func TestRunPauseReleasesOnlySelectedWorker(t *testing.T) {
	c := &Crew{workflow: contract.BoxWorkflow{Paused: true, Version: 1}, pauseChanged: make(chan struct{})}
	c.Reload(contract.BoxWorkflow{Paused: true, Version: 1, ReleasedSteps: []string{"a"}})
	if err := c.waitUnpaused(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"b", ""} {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := c.waitUnpaused(ctx, key); err != context.Canceled {
			t.Fatalf("%q escaped: %v", key, err)
		}
	}
}
