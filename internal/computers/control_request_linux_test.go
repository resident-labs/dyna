//go:build linux

package computers

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestHumanControlRequestKeepsReasonAndWaits(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	id := testSessionID(t, "handoff")
	if err := os.MkdirAll(Dir(id), 0700); err != nil {
		t.Fatal(err)
	}
	s := &supervisor{ctx: t.Context(), meta: Meta{SessionID: id}, state: Status{Control: ControlAgent}, compositor: &process{done: make(chan struct{})}, viewers: map[string]string{"watcher": "connection"}}
	if _, err := s.dispatch(t.Context(), Request{Op: "control", Mode: ControlHuman, Status: " Please sign in "}); err != nil {
		t.Fatal(err)
	}
	if s.state.Control != ControlHuman {
		t.Fatal("request did not pause agent control")
	}
	s.recordIntent("Agent is waiting")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, _, _, _, err := s.agentTarget(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("human wait bypassed: %v", err)
	}
	if _, err := s.dispatch(t.Context(), Request{Op: "view.close", ViewerID: "watcher"}); err != nil {
		t.Fatal(err)
	}
	if s.state.Control != ControlHuman {
		t.Fatal("passive viewer closed the handoff")
	}
	s.viewers["owner"] = "connection"
	if _, err := s.dispatch(t.Context(), Request{Op: "control", Mode: ControlHuman, ViewerID: "owner"}); err != nil {
		t.Fatal(err)
	}
	// A repeated request must not remove the owner's existing viewer lease.
	if _, err := s.dispatch(t.Context(), Request{Op: "control", Mode: ControlHuman, Status: "Please finish signing in"}); err != nil {
		t.Fatal(err)
	}
	if s.humanViewer != "owner" {
		t.Fatal("request removed human viewer ownership")
	}
	s.releaseViewer("owner")
	if s.state.Control != ControlAgent {
		t.Fatal("owner disconnect did not return control")
	}
}

func TestHumanControlRequestValidatesReason(t *testing.T) {
	s := &supervisor{ctx: t.Context(), state: Status{Control: ControlAgent}, compositor: &process{done: make(chan struct{})}}
	for _, r := range []Request{{Op: "control", Mode: ControlHuman}, {Op: "control", Mode: ControlAgent, Status: "Please sign in"}, {Op: "control", Mode: ControlHuman, Status: string(make([]byte, 81))}} {
		if _, err := s.dispatch(t.Context(), r); err == nil {
			t.Fatalf("invalid handoff accepted: %+v", r)
		}
	}
	if s.state.Control != ControlAgent {
		t.Fatal("invalid request changed control")
	}
}

func TestHumanRequestWaitIsBounded(t *testing.T) {
	s := &supervisor{state: Status{Control: ControlHuman}, compositor: &process{done: make(chan struct{})}}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	cancel()
	if _, _, _, _, _, err := s.agentTarget(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait: %v", err)
	}
}

func TestHumanControlRequestWithoutDesktopIsRejected(t *testing.T) {
	s := &supervisor{ctx: t.Context(), state: Status{Control: ControlAgent}}
	if _, err := s.dispatch(t.Context(), Request{Op: "control", Mode: ControlHuman, Status: "Please sign in"}); err == nil {
		t.Fatal("handoff accepted before a desktop exists")
	}
	if s.state.Control != ControlAgent || s.state.ControlReason != nil {
		t.Fatal("rejected request changed state")
	}
}
