package subagent

import (
	"net"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/engine"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/store"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

// serveSessionControls serves the engine's session calls over m from a fake
// engine, wrapped with the engine's caller policy, so a command reaches the
// engine's code path and never a live engine. The fake engine identifies a
// connection as whoever the test process poses as when it connects, as the
// engine identifies a real caller from its launch record.
func serveSessionControls(t *testing.T, m *subagent.Manager) {
	t.Helper()
	calls, err := engine.SessionCalls(roomsvc.New(notify.New(m.RoomStore(), m)))
	if err != nil {
		t.Fatal(err)
	}
	enginetest.ServeCallers(t, store.DataDir(), func(net.Conn) (websvc.Caller, error) {
		switch actor := authority.Current(); actor.Role {
		case authority.Session, authority.Root:
			return websvc.Caller{Kind: websvc.Session, Session: actor.Session}, nil
		case authority.Worker:
			return websvc.Caller{Kind: websvc.Worker, Run: actor.Run, Agent: actor.Agent}, nil
		case authority.Owner:
			return websvc.Caller{Kind: websvc.Owner}, nil
		}
		// A stamp the engine cannot match to a launch record.
		return websvc.Caller{Kind: websvc.Unverified}, nil
	}, calls)
}
