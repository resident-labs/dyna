package live

import "github.com/resident-labs/dyna/contract"

// Computer fields and watcher operations belong to the session lock.
type computerState struct {
	generation uint64
	state      contract.ComputerState
	room       contract.RoomID
	watchers   map[chan []byte]struct{}
	lastFrame  []byte
}

func (c *computerState) closeWatchers() {
	for ch := range c.watchers {
		delete(c.watchers, ch)
		close(ch)
	}
}

// ComputerWatch identifies one subscription and its original channel.
type ComputerWatch struct {
	computer *computerState
	channel  *Channel
	ch       chan []byte
}

func (w *ComputerWatch) Events() <-chan []byte { return w.ch }

// ComputerStream identifies the computer and channel when a frame stream started.
type ComputerStream struct {
	computer *computerState
	channel  *Channel
}

// ComputerState returns the current state. The caller holds the session lock.
func (s *Session) ComputerState() contract.ComputerState {
	if s.computer == nil {
		return contract.ComputerStopped
	}
	return s.computer.state
}

// SetComputer changes state after the corresponding event is recorded.
func (s *Session) SetComputer(room contract.RoomID, state contract.ComputerState) {
	if s.computer == nil {
		s.computer = &computerState{state: contract.ComputerStopped, room: room, watchers: make(map[chan []byte]struct{})}
	}
	s.computer.state = state
	if state == contract.ComputerStopped {
		s.computer.lastFrame = nil
		s.computer.closeWatchers()
		if s.channel != nil {
			s.channel.Wake()
		}
	}
}

// ClearComputer removes the computer and closes its watchers.
func (s *Session) ClearComputer() (contract.RoomID, bool) {
	c := s.computer
	if c == nil {
		return "", false
	}
	s.computer = nil
	c.closeWatchers()
	return c.room, c.state == contract.ComputerRunning
}

// WatchComputer adds a watcher. The caller holds the session lock and checks
// channel readiness; s.channel must be non-nil.
func (s *Session) WatchComputer() *ComputerWatch {
	c := s.computer
	if c == nil || c.state != contract.ComputerRunning {
		return nil
	}
	w := &ComputerWatch{computer: c, channel: s.channel, ch: make(chan []byte, 1)}
	if len(c.watchers) == 0 {
		c.generation = s.live.computerGeneration.Add(1)
		c.lastFrame = nil
		s.channel.Wake()
	} else if c.lastFrame != nil {
		w.ch <- c.lastFrame
	}
	c.watchers[w.ch] = struct{}{}
	return w
}

// RemoveComputerWatch closes a subscription if it is still active.
func (s *Session) RemoveComputerWatch(w *ComputerWatch) {
	if _, ok := w.computer.watchers[w.ch]; !ok {
		return
	}
	delete(w.computer.watchers, w.ch)
	close(w.ch)
	if len(w.computer.watchers) == 0 && s.channel == w.channel {
		w.channel.Wake()
	}
}

// ComputerWatchGeneration returns the desired box watch generation, or zero.
func (s *Session) ComputerWatchGeneration() uint64 {
	if s.computer == nil || len(s.computer.watchers) == 0 {
		return 0
	}
	return s.computer.generation
}

// ComputerStream captures frame stream state and reports whether a computer is attached.
func (s *Session) ComputerStream() (ComputerStream, bool) {
	if s.computer == nil {
		return ComputerStream{}, false
	}
	return ComputerStream{computer: s.computer, channel: s.channel}, true
}

// ComputerFrame publishes the latest frame to the current generation.
func (s *Session) ComputerFrame(generation uint64, frame []byte) {
	c := s.computer
	if c == nil || c.state != contract.ComputerRunning || c.generation != generation {
		return
	}
	c.lastFrame = frame
	// Drain before sending so each one-slot watcher keeps only the newest frame.
	for ch := range c.watchers {
		select {
		case <-ch:
		default:
		}
		ch <- frame
	}
}

// EndComputerStream closes watchers only if the stream still owns them.
func (s *Session) EndComputerStream(stream ComputerStream, generation uint64) {
	c, channel := stream.computer, stream.channel
	if c == nil || channel == nil || s.computer != c || s.channel != channel || c.generation != generation {
		return
	}
	c.lastFrame = nil
	c.closeWatchers()
	channel.ResetComputerWatch()
	channel.Wake()
}
