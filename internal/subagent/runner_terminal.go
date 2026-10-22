package subagent

import "context"

// terminalContext starts one deadline before the first waited final input
// read or terminal write. Finish, Flush and Close reuse its remaining time.
// Turn cancellation does not cancel persistence of its final facts.
func (w *runnerWrites) terminalContext() context.Context {
	w.terminalMu.Lock()
	defer w.terminalMu.Unlock()
	if w.terminal == nil {
		w.terminal, w.terminalCancel = context.WithTimeout(context.Background(), outboxClose)
		if w.out != nil {
			w.out.ArmTerminal(w.terminal)
		}
	}
	return w.terminal
}

// resumeActive disarms only an unexpired terminal deadline when a real
// continuation starts. Expired transport cannot resume engine sending.
func (w *runnerWrites) resumeActive() bool {
	w.terminalMu.Lock()
	defer w.terminalMu.Unlock()
	if w.terminal == nil {
		return true
	}
	if w.out != nil {
		if !w.out.DisarmTerminal() {
			return false
		}
	} else if w.terminal.Err() != nil {
		return false
	}
	w.terminalCancel()
	w.terminal, w.terminalCancel = nil, nil
	return true
}
