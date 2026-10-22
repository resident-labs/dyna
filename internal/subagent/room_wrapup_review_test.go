package subagent

import (
	"sync/atomic"
	"testing"
)

func TestReviewPauseDoesNotAcknowledgeUnstartedWrapUp(t *testing.T) {
	e := roomControlEnv(t)
	var enabled atomic.Bool
	e.m.WrapUpProviders = []WrapUpProvider{func(Info) []string {
		if enabled.Load() {
			return []string{"pending wrap-up must resume"}
		}
		return nil
	}}
	a, _ := e.start("SLEEP:1 RESPOND: initial")
	a, _ = e.wait(a.ID, 1)
	e.waitStatus(a.ID, StatusIdle)
	if _, err := e.m.PauseRoomSessions(e.ctx, a.RoomID); err != nil {
		t.Fatal(err)
	}
	enabled.Store(true)
	e.m.wrapUp(e.ctx, a.ID, 1)
	m, err := e.m.Store.ReadMeta(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m.WrapUpAfter >= 1 && m.Turns == 1 {
		t.Fatal("paused Wake acknowledged wrap-up without starting or durably saving its continuation")
	}
}
