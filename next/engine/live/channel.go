package live

import (
	"context"

	"github.com/resident-labs/dyna/contract"
)

// Channel owns the transient box channel state. Its session lock guards its fields.
type Channel struct {
	stop          context.CancelFunc
	wake          chan struct{}
	input         chan contract.ComputerInputBatch
	done          <-chan struct{}
	watching      uint64
	shellWatching uint64
	archiveRetry  bool
}

// Lock is transitional. B2b-2b-3 removes it when computer and shell state move into live.
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
	return &Channel{stop: stop, done: done, wake: make(chan struct{}, 1), input: make(chan contract.ComputerInputBatch, 64)}
}

// Wake notifies a box loop. The caller holds the session lock.
func (c *Channel) Wake() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Close completes one detached channel's teardown under the session lock.
// A nil channel still closes the detached watchers.
func (c *Channel) Close(shell []chan contract.ShellJobOutput) {
	for _, ch := range shell {
		close(ch)
	}
	if c != nil {
		c.stop()
	}
}

func (c *Channel) WakeSignal() <-chan struct{}                { return c.wake }
func (c *Channel) Inputs() <-chan contract.ComputerInputBatch { return c.input }

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

func (c *Channel) ResetComputerWatch() { c.watching = 0 }
func (c *Channel) RetryArchive()       { c.archiveRetry = true }
func (c *Channel) TakeArchiveRetry() bool {
	retry := c.archiveRetry
	c.archiveRetry = false
	return retry
}
