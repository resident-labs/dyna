package engine

import (
	"context"
	"dyna-agent/internal/doctor"
	"dyna-agent/internal/harness/sessionhost"
	"dyna-agent/internal/subagent"
	"strings"
	"time"
)

// sessionCallerPrefix starts the caller key of a Dyna session's harness process.
const sessionCallerPrefix = "session/"

// idleRelief stops idle harness processes of sessions that hold no turn work.
func idleRelief(sessions *subagent.Manager) doctor.Relief[sessionhost.IdleSession] {
	return doctor.Relief[sessionhost.IdleSession]{
		List: func(ctx context.Context) ([]doctor.Idle[sessionhost.IdleSession], error) {
			hosted, err := sessionhost.IdleSessions(ctx)
			var out []doctor.Idle[sessionhost.IdleSession]
			for _, h := range hosted {
				id, ok := strings.CutPrefix(h.Caller, sessionCallerPrefix)
				if !ok {
					continue // not a Dyna session: nothing says it is safe to stop
				}
				out = append(out, doctor.Idle[sessionhost.IdleSession]{Handle: h, Session: id, Idle: h.Idle})
			}
			return out, err
		},
		Busy: sessions.HoldsTurnWork,
		Unload: func(ctx context.Context, p doctor.Idle[sessionhost.IdleSession], minIdle time.Duration) (bool, error) {
			return sessionhost.UnloadIdle(ctx, p.Handle, minIdle)
		},
	}
}
