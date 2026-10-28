//go:build linux

package computers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPrepareRunPersistsIntentWithoutStartingDesktop(t *testing.T) {
	base, _ := startTestDaemon(t)
	ctx, cancel := context.WithTimeout(base, 2*time.Second)
	defer cancel()
	client := NewClient(testSessionID(t, "run-intent"))
	if _, err := client.Register(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	lease, err := client.PrepareRun(ctx, " Checking the command ")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	state, err := client.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if lease.SessionID != state.SessionID || state.State == StateRunning {
		t.Fatalf("run preparation started a desktop: %+v", state)
	}
	if state.LatestStatus == nil || state.LatestStatus.Text != "Checking the command" {
		t.Fatalf("intent was not saved: %+v", state.LatestStatus)
	}
	if state.Runs != 1 {
		t.Fatalf("active commands = %d, want 1", state.Runs)
	}
	events, err := client.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case event := <-events:
			if event.Type == "runs" && event.SessionID == lease.SessionID && event.Status != nil && event.Status.Runs == 0 {
				var meta Meta
				if err := client.Call(ctx, Request{Op: "run.prepare", Status: "Legacy command"}, &meta); err == nil {
					t.Fatal("new daemon accepted an unprotected legacy run")
				}
				if err := client.Call(ctx, Request{Op: "run.lease"}, &meta); err == nil {
					t.Fatal("run preparation accepted missing intent")
				}
				return
			}
		case <-ctx.Done():
			t.Fatal("run disconnect did not release its lease:", ctx.Err())
		}
	}
}

func TestRestartAdmissionBlocksNewRunLeases(t *testing.T) {
	id := recordTestID(t)
	done := make(chan struct{})
	close(done)
	s := &supervisor{
		meta: Meta{SessionID: id}, state: Status{SessionID: id, Control: ControlAgent},
		inputGate: make(chan struct{}, 1), runs: map[string]struct{}{"active": {}},
		compositor: &process{done: done}, emit: func(Event) {},
	}
	s.state.Runs = 1
	d := &daemon{computers: map[string]*supervisor{id: s}}
	if reason := d.beginRestart().Reason; !strings.Contains(reason, "foreground command") {
		t.Fatalf("active run did not defer restart: %q", reason)
	}
	delete(s.runs, "active")
	s.state.Runs = 0
	if reason := d.beginRestart().Reason; reason != "" {
		t.Fatalf("idle computer did not admit restart: %q", reason)
	}
	ctx := context.WithValue(context.Background(), connectionKey{}, "new-run")
	if _, err := d.leaseRun(ctx, s, Request{Status: "Checking restart admission"}); err == nil || !strings.Contains(err.Error(), "restart is in progress") {
		t.Fatalf("new run entered after restart admission closed: %v", err)
	}
}

func TestPrepareRunWaitsForHumanControlAndReleasesItsGate(t *testing.T) {
	id := recordTestID(t)
	deadline, stop := context.WithTimeout(t.Context(), 30*time.Second)
	defer stop()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	intent := make(chan struct{}, 1)
	s := &supervisor{
		ctx:        t.Context(),
		meta:       Meta{SessionID: id},
		state:      Status{SessionID: id, Control: ControlHuman},
		inputGate:  make(chan struct{}, 1),
		compositor: &process{done: make(chan struct{})},
		emit: func(event Event) {
			if event.Type == "status" {
				intent <- struct{}{}
			}
		},
	}
	ended := make(chan error, 1)
	go func() {
		_, err := s.prepareRun(ctx, Request{Status: "Waiting for control"})
		ended <- err
	}()
	select {
	case <-intent:
	case <-deadline.Done():
		t.Fatal("waiting for run intent:", deadline.Err())
	}
	saved := savedStatus(id)
	if saved.SessionID != id || saved.Control != ControlHuman || saved.LatestStatus == nil || saved.LatestStatus.Text != "Waiting for control" || saved.LatestStatus.At.IsZero() {
		t.Fatalf("intent was not saved for the waiting computer: %+v", saved)
	}
	cancel()
	select {
	case err := <-ended:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("human control was bypassed: %v", err)
		}
	case <-deadline.Done():
		t.Fatal("waiting for cancellation:", deadline.Err())
	}
	select {
	case s.inputGate <- struct{}{}:
		<-s.inputGate
	default:
		t.Fatal("canceled run retained its input gate")
	}
}
