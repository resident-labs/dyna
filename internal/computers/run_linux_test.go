//go:build linux

package computers

import (
	"context"
	"errors"
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
	var meta Meta
	if err := client.Call(ctx, Request{Op: "run.prepare", Status: " Checking the command "}, &meta); err != nil {
		t.Fatal(err)
	}
	state, err := client.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if meta.SessionID != state.SessionID || state.State == StateRunning {
		t.Fatalf("run preparation started a desktop: %+v", state)
	}
	if state.LatestStatus == nil || state.LatestStatus.Text != "Checking the command" {
		t.Fatalf("intent was not saved: %+v", state.LatestStatus)
	}
	if err := client.Call(ctx, Request{Op: "run.prepare"}, &meta); err == nil {
		t.Fatal("run preparation accepted missing intent")
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
