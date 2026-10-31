package engine

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestComputerWatchFanoutAndState(t *testing.T) {
	s, room, id, ctx := setup(t)
	connectAgent(ctx, t, s, id)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: id}
	channel := &boxChannel{control: make(chan contract.BoxMessage, 2)}
	s.channels[id] = channel
	if _, err := s.ComputerWatch(ctx, id); err == nil || err.Code != contract.ErrNotFound {
		t.Fatalf("without computer: %v", err)
	}
	if _, err := s.ComputerChanged(ctx, caller, contract.ComputerChangedRequest{To: contract.ComputerRunning}); err != nil {
		t.Fatal(err)
	}
	firstCtx, cancelFirst := context.WithCancel(ctx)
	secondCtx, cancelSecond := context.WithCancel(ctx)
	defer cancelFirst()
	defer cancelSecond()
	first, err := s.ComputerWatch(firstCtx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := (<-channel.control).Body.(contract.ComputerWatchStart); !ok {
		t.Fatal("first watcher did not start")
	}
	second, err := s.ComputerWatch(secondCtx, id)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-channel.control:
		t.Fatal("second watcher started again")
	default:
	}
	send := func(frame string) {
		frames := new(bytes.Buffer)
		_ = binary.Write(frames, binary.BigEndian, uint32(len(frame)))
		frames.WriteString(frame)
		if err := s.ComputerFrames(ctx, caller, frames); err != nil {
			t.Fatal(err)
		}
	}
	send("one")
	if string(<-second) != "one" {
		t.Fatal("first frame missing")
	}
	send("two")
	if string(<-second) != "two" || string(<-first) != "one" {
		t.Fatal("slow watcher blocked fanout")
	}
	cancelFirst()
	// Cancellation is synchronized by the stopped channel.
	if _, ok := <-first; ok {
		t.Fatal("watcher stayed open")
	}
	select {
	case <-channel.control:
		t.Fatal("stopped before the last watcher")
	default:
	}
	cancelSecond()
	if _, ok := <-second; ok {
		t.Fatal("watcher stayed open")
	}
	if _, ok := (<-channel.control).Body.(contract.ComputerWatchStop); !ok {
		t.Fatal("last watcher did not stop")
	}
	list, problem := s.SessionList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionListRequest{Room: room})
	if problem != nil {
		t.Fatal(problem)
	}
	found := false
	for _, session := range list.Sessions {
		if session.ID == id && session.Computer == contract.ComputerRunning {
			found = true
		}
	}
	if !found {
		t.Fatal("session list did not report running")
	}
	_, problem = s.ComputerChanged(ctx, caller, contract.ComputerChangedRequest{To: contract.ComputerStopped})
	if problem != nil {
		t.Fatal(problem)
	}
	events, e := s.store.Events(ctx, room, 0, 256, []contract.EventType{"computer.changed"})
	if e != nil || len(events) != 2 {
		t.Fatalf("computer events: %v, %v", events, e)
	}
	if events[0].Body.(contract.ComputerChanged).To != contract.ComputerRunning || events[1].Body.(contract.ComputerChanged).To != contract.ComputerStopped {
		t.Fatal("incorrect computer events")
	}
}

func TestComputerWatchRequiresReadyBox(t *testing.T) {
	s, _, id, ctx := setup(t)
	if _, err := s.ComputerWatch(ctx, id); err == nil || err.Code != contract.ErrConflict {
		t.Fatalf("unready box: %v", err)
	}
}

func TestComputerChannelCloseEndsWatch(t *testing.T) {
	s, room, id, ctx := setup(t)
	connectAgent(ctx, t, s, id)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: id}
	s.channels[id] = &boxChannel{control: make(chan contract.BoxMessage, 2)}
	if _, err := s.ComputerChanged(ctx, caller, contract.ComputerChangedRequest{To: contract.ComputerRunning}); err != nil {
		t.Fatal(err)
	}
	watch, err := s.ComputerWatch(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.clearComputerLocked(id)
	s.mu.Unlock()
	if _, ok := <-watch; ok {
		t.Fatal("watch stayed open")
	}
	events, e := s.store.Events(ctx, room, 0, 256, []contract.EventType{"computer.changed"})
	if e != nil || len(events) != 2 || events[1].Body.(contract.ComputerChanged).To != contract.ComputerStopped {
		t.Fatalf("stop event: %v %v", events, e)
	}
}
