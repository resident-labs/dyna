package engine

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestComputerWatchFanoutAndState(t *testing.T) {
	s, room, id, ctx := setup(t)
	connectAgent(ctx, t, s, id)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: id}
	channel := &boxChannel{wake: make(chan struct{}, 1)}
	s.mu.Lock()
	s.sessionLocked(id).channel = channel
	s.mu.Unlock()
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
	<-channel.wake
	second, err := s.ComputerWatch(secondCtx, id)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	generation := s.sessions[id].computer.generation
	s.mu.Unlock()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	done := make(chan *contract.Error, 1)
	go func() { done <- s.ComputerFrames(ctx, caller, generation, reader) }()
	send := func(frame string) {
		if err := binary.Write(writer, binary.BigEndian, uint32(len(frame))); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(frame)); err != nil {
			t.Fatal(err)
		}
	}
	send("one")
	if string(<-second) != "one" {
		t.Fatal("first frame missing")
	}
	send("two")
	if string(<-second) != "two" || string(<-first) != "two" {
		t.Fatal("slow watcher did not receive the newest frame")
	}
	lateCtx, cancelLate := context.WithCancel(ctx)
	defer cancelLate()
	late, err := s.ComputerWatch(lateCtx, id)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-late:
		if string(frame) != "two" {
			t.Fatalf("cached frame: %q", frame)
		}
	default:
		t.Fatal("late viewer did not receive the cached frame")
	}
	cancelLate()
	<-late
	cancelFirst()
	// Cancellation is synchronized by the stopped channel.
	if _, ok := <-first; ok {
		t.Fatal("watcher stayed open")
	}
	s.mu.Lock()
	remaining := len(s.sessions[id].computer.watchers)
	s.mu.Unlock()
	if remaining != 1 {
		t.Fatalf("watchers after first cancellation: %d", remaining)
	}
	cancelSecond()
	if _, ok := <-second; ok {
		t.Fatal("watcher stayed open")
	}
	<-channel.wake
	_ = writer.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
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

func TestComputerWatchControlsBoxChannel(t *testing.T) {
	s, _, id, ctx := setup(t)
	connectAgent(ctx, t, s, id)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: id}
	boxCtx, stopBox := context.WithCancel(ctx)
	defer stopBox()
	messages, err := s.BoxChannel(boxCtx, caller)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ComputerChanged(ctx, caller, contract.ComputerChangedRequest{To: contract.ComputerRunning}); err != nil {
		t.Fatal(err)
	}
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	frames, err := s.ComputerWatch(watchCtx, id)
	if err != nil {
		t.Fatal(err)
	}
	wait := func(want contract.BoxMessageType) {
		t.Helper()
		for m := range messages {
			if m.Body.BoxMessageType() == want {
				return
			}
		}
		t.Fatalf("box channel closed before %s", want)
	}
	wait("computer.watch.start")
	if err := s.ComputerFrames(ctx, caller, s.sessions[id].computer.generation, bytes.NewReader(nil)); err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-frames:
		if ok {
			t.Fatal("unexpected frame")
		}
	default:
		t.Fatal("failed frame stream kept watcher open")
	}
	frames, err = s.ComputerWatch(watchCtx, id)
	if err != nil {
		t.Fatal(err)
	}
	wait("computer.watch.start")
	cancel()
	if _, ok := <-frames; ok {
		t.Fatal("watcher stayed open")
	}
	wait("computer.watch.stop")
}

func TestComputerInputReachesBoxAndRefusesSessions(t *testing.T) {
	s, _, id, ctx := setup(t)
	connectAgent(ctx, t, s, id)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: id}
	boxCtx, stopBox := context.WithCancel(ctx)
	defer stopBox()
	messages, err := s.BoxChannel(boxCtx, caller)
	if err != nil {
		t.Fatal(err)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	req := contract.ComputerInputRequest{Session: id, Frame: contract.FrameSize{Width: 10, Height: 10}, Events: []contract.ComputerInput{{Kind: contract.InputKeyDown, Key: "a"}}}
	if _, err := s.ComputerInput(ctx, owner, req); err == nil || err.Code != contract.ErrNotFound {
		t.Fatalf("stopped computer: %v", err)
	}
	if _, err := s.ComputerChanged(ctx, caller, contract.ComputerChangedRequest{To: contract.ComputerRunning}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ComputerInput(ctx, caller, req); err == nil || err.Code != contract.ErrForbidden {
		t.Fatalf("session caller: %v", err)
	}
	if _, err := s.ComputerInput(ctx, owner, req); err != nil {
		t.Fatal(err)
	}
	for m := range messages {
		if batch, ok := m.Body.(contract.ComputerInputBatch); ok {
			if batch.Frame != req.Frame || batch.Events[0].Key != "a" {
				t.Fatalf("batch %+v", batch)
			}
			return
		}
	}
	t.Fatal("box channel closed before computer.input")
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
	s.mu.Lock()
	s.sessionLocked(id).channel = &boxChannel{wake: make(chan struct{}, 1)}
	s.mu.Unlock()
	if _, err := s.ComputerChanged(ctx, caller, contract.ComputerChangedRequest{To: contract.ComputerRunning}); err != nil {
		t.Fatal(err)
	}
	watch, err := s.ComputerWatch(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	state := s.sessionLocked(id)
	s.mu.Unlock()
	state.computerLock.Lock()
	s.mu.Lock()
	stopRoom, stopped := s.clearComputerLocked(id)
	s.mu.Unlock()
	if stopped {
		if err := s.computerEvent(ctx, id, stopRoom, contract.ComputerStopped); err != nil {
			t.Fatal(err)
		}
	}
	state.computerLock.Unlock()
	if _, ok := <-watch; ok {
		t.Fatal("watch stayed open")
	}
	events, e := s.store.Events(ctx, room, 0, 256, []contract.EventType{"computer.changed"})
	if e != nil || len(events) != 2 || events[1].Body.(contract.ComputerChanged).To != contract.ComputerStopped {
		t.Fatalf("stop event: %v %v", events, e)
	}
}

func TestOldComputerStreamDoesNotEndNewWatch(t *testing.T) {
	s, _, id, ctx := setup(t)
	connectAgent(ctx, t, s, id)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: id}
	s.mu.Lock()
	s.sessionLocked(id).channel = &boxChannel{wake: make(chan struct{}, 1)}
	s.mu.Unlock()
	if _, err := s.ComputerChanged(ctx, caller, contract.ComputerChangedRequest{To: contract.ComputerRunning}); err != nil {
		t.Fatal(err)
	}
	firstCtx, cancelFirst := context.WithCancel(ctx)
	first, err := s.ComputerWatch(firstCtx, id)
	if err != nil {
		t.Fatal(err)
	}
	generation := s.sessions[id].computer.generation
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	done := make(chan *contract.Error, 1)
	go func() { done <- s.ComputerFrames(ctx, caller, generation, reader) }()
	if err := binary.Write(writer, binary.BigEndian, uint32(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if string(<-first) != "x" {
		t.Fatal("old stream did not start")
	}
	cancelFirst()
	if _, ok := <-first; ok {
		t.Fatal("first watcher remained open")
	}
	secondCtx, cancelSecond := context.WithCancel(ctx)
	defer cancelSecond()
	second, err := s.ComputerWatch(secondCtx, id)
	if err != nil {
		t.Fatal(err)
	}
	if s.sessions[id].computer.generation == generation {
		t.Fatal("generation did not change")
	}
	if err := binary.Write(writer, binary.BigEndian, uint32(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("y")); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-second:
		if !ok {
			t.Fatal("old stream closed new watcher")
		}
		t.Fatal("old stream delivered a stale frame")
	default:
	}
}

func TestShellWatchGenerationAndStop(t *testing.T) {
	s, _, id, ctx := setup(t)
	connectAgent(ctx, t, s, id)
	channelCtx, cancelChannel := context.WithCancel(ctx)
	defer cancelChannel()
	messages, problem := s.BoxChannel(channelCtx, contract.Principal{Kind: contract.PrincipalSession, Session: id})
	if problem != nil {
		t.Fatal(problem)
	}
	watchCtx, cancel := context.WithCancel(ctx)
	ch, problem := s.ShellWatch(watchCtx, id)
	if problem != nil {
		t.Fatal(problem)
	}
	var start contract.ShellWatchStart
	for message := range messages {
		if body, ok := message.Body.(contract.ShellWatchStart); ok {
			start = body
			break
		}
	}
	if start.Generation == 0 {
		t.Fatal("box channel closed before start")
	}
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: id}
	s.ShellOutput(ctx, caller, contract.ShellOutputRequest{Generation: start.Generation - 1, Jobs: []contract.ShellJobOutput{{Text: "old"}}})
	s.ShellOutput(ctx, caller, contract.ShellOutputRequest{Generation: start.Generation, Jobs: []contract.ShellJobOutput{{Text: "new"}}})
	if got := <-ch; got.Text != "new" {
		t.Fatalf("got %+v", got)
	}
	cancel()
	<-ch
	for message := range messages {
		if _, ok := message.Body.(contract.ShellWatchStop); ok {
			return
		}
	}
	t.Fatal("box channel closed before stop")
}

// A replaced box rejects input even when its queue has room.
func TestComputerInputReplacement(t *testing.T) {
	done := make(chan struct{})
	handle := computerInputHandle{queue: make(chan contract.ComputerInputBatch, 1), done: done}
	close(done)
	problem := handle.send(context.Background(), contract.ComputerInputBatch{})
	if problem == nil || problem.Code != contract.ErrConflict || problem.Message != "box replaced" {
		t.Fatalf("replacement: %v", problem)
	}
}

func TestComputerInputFullQueue(t *testing.T) {
	handle := computerInputHandle{queue: make(chan contract.ComputerInputBatch, 1), done: make(chan struct{})}
	handle.queue <- contract.ComputerInputBatch{}
	problem := handle.send(context.Background(), contract.ComputerInputBatch{})
	if problem == nil || problem.Code != contract.ErrConflict || problem.Message != "input queue is full" {
		t.Fatalf("full queue: %v", problem)
	}
}
