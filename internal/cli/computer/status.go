package computer

import "dyna-agent/internal/computers"

// Explain only what the reported state proves. A renderer name does not tell
// us whether a hardware video encoder is available.
func statusDoctor(state computers.Status) string {
	if state.Reason != "" {
		return state.Reason
	}
	if state.State == computers.StateStarting {
		return "desktop starting"
	}
	if state.State != computers.StateRunning {
		return "desktop not running"
	}
	if state.Renderer == "" {
		return "graphics unavailable"
	}
	if !state.Stream.Running {
		return "stream not running"
	}
	return ""
}
