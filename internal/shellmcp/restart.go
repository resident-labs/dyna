package shellmcp

import "sync"

// restartGate runs a server's restart once the server is quiet. A call that
// fails because this binary is older than the state it serves asks for the
// restart; the gate holds it until no call is in flight and the server waits
// on its client with nothing read ahead. The restart replaces the process, so
// any call still running, or any request already read from stdin, would never
// be answered. Requests that arrive meanwhile are answered as usual, and the
// restart waits for them too (D-171).
type restartGate struct {
	tools *Tools

	mu       sync.Mutex
	inflight int
	waiting  bool // the server blocks on its client with no input read ahead
	pending  bool // an outdated call asked for a restart
	done     bool
}

func newRestartGate(tools *Tools) *restartGate { return &restartGate{tools: tools} }

// begin notes a call that started.
func (g *restartGate) begin() {
	g.mu.Lock()
	g.inflight++
	g.mu.Unlock()
}

// end notes a call that answered, and restarts if that was the last thing the
// restart waited for.
func (g *restartGate) end() {
	g.mu.Lock()
	g.inflight--
	g.mu.Unlock()
	g.try()
}

// callEnded asks for the restart when this server's outdated flag is set.
func (g *restartGate) callEnded() {
	if g.tools.Restart == nil || g.tools.outdated == nil || !g.tools.outdated.Load() {
		return
	}
	g.mu.Lock()
	g.pending = true
	g.mu.Unlock()
}

// idle records whether the server now waits on its client with nothing read
// ahead, and restarts if a restart was waiting for that.
func (g *restartGate) idle(waiting bool) {
	g.mu.Lock()
	g.waiting = waiting
	g.mu.Unlock()
	if waiting {
		g.try()
	}
}

// try runs the restart when every condition holds. The restart returns only
// when it failed; the gate then stays quiet, as the server keeps serving with
// the error it already reported.
func (g *restartGate) try() {
	g.mu.Lock()
	ready := g.pending && !g.done && g.inflight == 0 && g.waiting
	if ready {
		g.done = true
	}
	g.mu.Unlock()
	if ready {
		g.tools.Restart()
	}
}
