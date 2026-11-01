package live

import "github.com/resident-labs/dyna/contract"

type shellWatch struct {
	generation uint64
	watchers   map[chan contract.ShellJobOutput]struct{}
}

// ShellWatch identifies a subscription and its original channel.
type ShellWatch struct {
	state   *shellWatch
	channel *Channel
	ch      chan contract.ShellJobOutput
}

func (w *ShellWatch) Events() <-chan contract.ShellJobOutput { return w.ch }

// WatchShell adds a watcher. The caller holds the session lock and checks
// channel readiness; s.channel must be non-nil.
func (s *Session) WatchShell() *ShellWatch {
	if s.shell == nil {
		s.shell = &shellWatch{watchers: make(map[chan contract.ShellJobOutput]struct{})}
	}
	w := &ShellWatch{state: s.shell, channel: s.channel, ch: make(chan contract.ShellJobOutput, 128)}
	s.shell.generation = s.live.shellGeneration.Add(1)
	s.shell.watchers[w.ch] = struct{}{}
	s.channel.Wake()
	return w
}

// RemoveShellWatch closes an active watcher.
func (s *Session) RemoveShellWatch(w *ShellWatch) {
	if _, ok := w.state.watchers[w.ch]; !ok {
		return
	}
	delete(w.state.watchers, w.ch)
	close(w.ch)
	if len(w.state.watchers) == 0 && s.channel == w.channel {
		w.channel.Wake()
	}
}

// ClearShell closes all watchers under the session lock.
func (s *Session) ClearShell() {
	if s.shell == nil {
		return
	}
	for ch := range s.shell.watchers {
		delete(s.shell.watchers, ch)
		close(ch)
	}
	s.shell = nil
}

// ShellWatchGeneration returns the desired box watch generation, or zero.
func (s *Session) ShellWatchGeneration() uint64 {
	if s.shell == nil || len(s.shell.watchers) == 0 {
		return 0
	}
	return s.shell.generation
}

// ShellOutput delivers matching output and closes any watcher that falls behind.
func (s *Session) ShellOutput(generation uint64, jobs []contract.ShellJobOutput) {
	w := s.shell
	if w == nil || w.generation != generation {
		return
	}
watcher:
	for ch := range w.watchers {
		for _, job := range jobs {
			select {
			case ch <- job:
			default:
				delete(w.watchers, ch)
				close(ch)
				continue watcher
			}
		}
	}
	if len(w.watchers) == 0 && s.channel != nil {
		s.channel.Wake()
	}
}
