package notify

import (
	"testing"

	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

func TestQuietPostDoesNotPrintSessionQueued(t *testing.T) {
	e := newTestEnv(t)
	quiet := e.svc.deliver(room.Message{ID: "msg_quiet", Wake: false}, recipient{kind: toSession, addr: "work:helper", session: subagent.Info{}})
	if quiet.Delivery != "session-quiet" {
		t.Fatalf("quiet delivery = %+v, want session-quiet", quiet)
	}
	waking := e.svc.deliver(room.Message{ID: "msg_wake", Wake: true}, recipient{kind: toSession, addr: "work:helper", session: subagent.Info{}})
	if waking.Delivery != "session-queued" {
		t.Fatalf("waking delivery = %+v, want session-queued", waking)
	}
}
