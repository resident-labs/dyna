package live

import (
	"context"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// Channel owns the transient box channel state. Its session lock guards its fields.
type Channel struct {
	stop          context.CancelFunc
	wake          chan struct{}
	input         chan contract.ComputerInputBatch
	asks          chan uint64
	asked         uint64
	answers       map[uint64]chan contract.BoxReading
	done          <-chan struct{}
	watching      uint64
	shellWatching uint64
	archiveRetry  bool
	// The compaction asked of this box before a quiet stop: the last turn it was asked after, whether the box loop still has
	// to send it, and until when a stop waits for the answer.
	compactTurn  contract.TurnID
	compactSend  bool
	compactUntil time.Time
}

// Lock is transitional while channel coordination and terminal state remain in engine.
// It returns false for a removed session without retaining the lock.
func (s *Session) Lock() bool {
	s.mu.Lock()
	if s.removed {
		s.mu.Unlock()
		return false
	}
	return true
}

// Unlock releases the transitional session lock.
func (s *Session) Unlock() { s.mu.Unlock() }

// Connected reports whether a live handle has a box channel.
func (s *Session) Connected() (connected, open bool) {
	if !s.Lock() {
		return false, false
	}
	defer s.Unlock()
	return s.channel != nil, true
}

// Channel returns the attached channel. The caller holds the session lock.
func (s *Session) Channel() *Channel { return s.channel }

// SetChannel replaces the channel. The caller holds the session lock.
func (s *Session) SetChannel(channel *Channel) { s.channel = channel }

// NewChannel makes a box channel. The caller holds its session lock.
func NewChannel(stop context.CancelFunc, done <-chan struct{}) *Channel {
	return &Channel{stop: stop, done: done, wake: make(chan struct{}, 1), input: make(chan contract.ComputerInputBatch, 64),
		asks: make(chan uint64, 8), answers: make(map[uint64]chan contract.BoxReading)}
}

// Wake notifies a box loop. The caller holds the session lock.
func (c *Channel) Wake() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Close stops a detached channel under the session lock. It accepts a nil channel.
func (c *Channel) Close() {
	if c != nil {
		c.stop()
	}
}

func (c *Channel) WakeSignal() <-chan struct{}                { return c.wake }
func (c *Channel) Inputs() <-chan contract.ComputerInputBatch { return c.input }
func (c *Channel) Asks() <-chan uint64                        { return c.asks }

// AskStats queues a question for the box and returns where its answer arrives. A full queue means the box stopped
// reading, so nothing arrives and the wait runs out. The caller holds the session lock, and later calls ForgetStats.
func (c *Channel) AskStats() (uint64, <-chan contract.BoxReading) {
	c.asked++
	answer := make(chan contract.BoxReading, 1)
	select {
	case c.asks <- c.asked:
		c.answers[c.asked] = answer
	default:
	}
	return c.asked, answer
}

// AnswerStats gives an answer to its question. The caller holds the session lock.
func (c *Channel) AnswerStats(ask uint64, reading contract.BoxReading) {
	if answer, ok := c.answers[ask]; ok {
		delete(c.answers, ask)
		answer <- reading
	}
}

// ForgetStats drops a question nobody waits for any more. The caller holds the session lock.
func (c *Channel) ForgetStats(ask uint64) { delete(c.answers, ask) }

// SendInput queues a batch. The caller holds the session lock.
func (c *Channel) SendInput(ctx context.Context, batch contract.ComputerInputBatch) *contract.Error {
	select {
	case <-c.done:
		return &contract.Error{Code: contract.ErrConflict, Message: "box replaced"}
	default:
	}
	select {
	case <-c.done:
		return &contract.Error{Code: contract.ErrConflict, Message: "box replaced"}
	case <-ctx.Done():
		return &contract.Error{Code: contract.ErrUnavailable, Message: ctx.Err().Error()}
	case c.input <- batch:
		return nil
	default:
		return &contract.Error{Code: contract.ErrConflict, Message: "input queue is full"}
	}
}

// WatchChanges sets the last requested watch generations.
func (c *Channel) WatchChanges(computer, shell uint64) (bool, bool) {
	computerChanged, shellChanged := c.watching != computer, c.shellWatching != shell
	c.watching, c.shellWatching = computer, shell
	return computerChanged, shellChanged
}

// AskCompact asks the box loop to send a compaction once per turn, and reports whether the answer is still awaited at now.
// The caller holds the session lock.
func (c *Channel) AskCompact(turn contract.TurnID, until, now time.Time) bool {
	if c.compactTurn != turn {
		c.compactTurn, c.compactSend, c.compactUntil = turn, true, until
		c.Wake()
	}
	return now.Before(c.compactUntil)
}

// TakeCompact returns a compaction waiting to be sent. The caller holds the session lock.
func (c *Channel) TakeCompact() (contract.TurnID, bool) {
	send := c.compactSend
	c.compactSend = false
	return c.compactTurn, send
}

// CompactAnswered ends the wait for a compaction. The caller holds the session lock.
func (c *Channel) CompactAnswered() { c.compactUntil = time.Time{} }

func (c *Channel) ResetComputerWatch() { c.watching = 0 }
func (c *Channel) RetryArchive()       { c.archiveRetry = true }
func (c *Channel) TakeArchiveRetry() bool {
	retry := c.archiveRetry
	c.archiveRetry = false
	return retry
}
