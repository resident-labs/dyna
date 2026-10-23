package roomsvc

import "dyna-agent/internal/hooks"

// scanHookSpool routes spool changes through the same store watch as other
// worker inputs. The engine's startup pass handles the initial state.
func (s *Service) scanHookSpool(previous *string, first bool, emit func(Event)) {
	version := hooks.New().SpoolVersion()
	if version == *previous {
		return
	}
	*previous = version
	if !first {
		emit(Event{Kind: "hooks", WorkerWake: true})
	}
}
