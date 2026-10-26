package computers

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestActionNeverBeginsUnderHumanControl(t *testing.T) {
	r := &recorder{}
	g := newGuardedInput(r)
	_, cancel := context.WithCancel(context.Background())
	s := &supervisor{}
	s.state.Control = ControlHuman
	if s.beginAct(g, cancel) || s.actPointer != nil {
		t.Fatal("an action began while a person had control")
	}
}

// The takeover can land right after beginAct returns, before the first event.
func TestTakeoverRightAfterBeginActStopsTheFirstEvent(t *testing.T) {
	r := &recorder{}
	g := newGuardedInput(r)
	_, cancel := context.WithCancel(context.Background())
	s := &supervisor{}
	s.state.Control = ControlAgent
	if !s.beginAct(g, cancel) || s.actPointer != g {
		t.Fatal("the action did not begin under agent control")
	}
	s.mu.Lock()
	s.state.Control = ControlHuman
	s.interruptActLocked()
	s.mu.Unlock()
	err := pointerButtonAction(g, parseActions(t, `[{"type":"pointer_down","x":5,"y":6}]`)[0])
	if !errors.Is(err, errControlLost) || strings.Join(r.log, "|") != "" {
		t.Fatalf("err = %v, events = %v, want no input after the takeover", err, r.log)
	}
}
