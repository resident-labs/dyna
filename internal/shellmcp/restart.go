package shellmcp

import (
	"os"
	"sync"
)

// restartGate runs a server's restart at a moment that loses nothing. A call
// that fails because this binary is older than the state it serves asks for a
// restart. The restart replaces the process, so it must not run while a call
// is in flight, which would never answer, and it must not run after the server
// has taken bytes of a request off stdin, which the new process would never
// see. Only the read loop restarts: it waits for input with poll, which
// consumes nothing, and runs the restart when no call is in flight and no
// request has been read or is being read. A request that arrives first is read
// and answered as usual, and the restart waits for the next quiet moment
// (D-171).
type restartGate struct {
	tools *Tools
	wake  wakeup

	mu       sync.Mutex
	inflight int
	pending  bool // an outdated call asked for a restart
	spent    bool // the restart ran and failed; it is not tried again
}

func newRestartGate(tools *Tools) *restartGate {
	g := &restartGate{tools: tools}
	if tools.Restart != nil && tools.outdated != nil {
		g.wake = newWakeup()
	}
	return g
}

// enabled reports whether this server can restart at all.
func (g *restartGate) enabled() bool { return g.wake != nil }

// close releases the wake-up descriptors.
func (g *restartGate) close() {
	if g.wake != nil {
		g.wake.close()
	}
}

// begin notes a call that started.
func (g *restartGate) begin() {
	g.mu.Lock()
	g.inflight++
	g.mu.Unlock()
}

// end notes a call that answered and wakes the read loop to re-check.
func (g *restartGate) end() {
	g.mu.Lock()
	g.inflight--
	g.mu.Unlock()
	g.signal()
}

// callEnded asks for the restart when this server's outdated flag is set.
func (g *restartGate) callEnded() {
	if !g.enabled() || !g.tools.outdated.Load() {
		return
	}
	g.mu.Lock()
	g.pending = true
	g.mu.Unlock()
	g.signal()
}

func (g *restartGate) signal() {
	if g.wake != nil {
		g.wake.signal()
	}
}

// ready reports whether the restart may run now, and marks it spent.
func (g *restartGate) ready() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.pending || g.spent || g.inflight > 0 {
		return false
	}
	g.spent = true
	return true
}

// awaitInput blocks until stdin has bytes to read, running the restart first
// whenever it becomes allowed. It reads nothing itself. The restart returns
// only when it failed; the server then keeps serving with the error it
// already reported.
func (g *restartGate) awaitInput(in *os.File) {
	for {
		if g.ready() {
			g.tools.Restart()
			return
		}
		if g.wake.wait(in) {
			return
		}
	}
}
