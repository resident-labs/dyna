package subagent

import (
	"dyna-agent/internal/room"
	"os"
	"strings"
	"testing"
	"time"
)

func TestReviewRecoveredHeldPromptDoesNotAcknowledgeUnsentNewPost(t *testing.T) {
	e := roomControlEnv(t)
	a, _ := e.start("SLEEP:1 RESPOND: initial")
	a, _ = e.wait(a.ID, 1)
	e.waitStatus(a.ID, StatusIdle)
	_, err := e.m.Store.UpdateTurn(a.ID, 1, func(t *Turn) {
		t.Status = TurnRunning
		t.Ended = time.Time{}
		t.RoomHeld = true
		t.HeldAccepted = false
		t.HeldPrompt = "RESPOND: recovered original request"
	})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := e.m.rooms().Post(a.RoomID, room.PostInput{From: a.Room, To: []string{a.Address()}, Text: "NEW_POST_MUST_REACH_PROVIDER"})
	if err != nil {
		t.Fatal(err)
	}
	lock, err := e.m.Store.tryTurnLock(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.m.RunTurn(e.ctx, a.ID, 1, lock); err != nil {
		t.Fatal(err)
	}
	done, err := e.m.Store.ReadTurn(a.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range done.Steering {
		if st.ID == msg.ID && st.Delivered && !strings.Contains(done.Result.(string), "NEW_POST_MUST_REACH_PROVIDER") {
			t.Fatalf("new post was acknowledged but omitted from provider prompt: result=%q steering=%+v", done.Result, done.Steering)
		}
	}
}

func TestReviewResumeKeepsRecoveryUntilQueuedWorkerAccepts(t *testing.T) {
	e := roomControlEnv(t)
	a, _ := e.start("SLEEP:1 RESPOND: initial")
	a, _ = e.wait(a.ID, 1)
	e.waitStatus(a.ID, StatusIdle)
	if _, err := e.m.PauseRoomSessions(e.ctx, a.RoomID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.send(a.ID, "SLEEP:1 RESPOND: pending after resume", WakeOptions{}); err != nil {
		t.Fatal(err)
	}
	e.m.Spawner = recordRoomSpawn{start: func(_ *Manager, _ string, _ int, f *os.File) error { return f.Close() }}
	if _, err := e.m.ResumeRoomSessions(e.ctx, a.RoomID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.TickSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	before, _ := e.m.rooms().SessionPause(a.RoomID)
	if err := e.m.runRoomResume(e.ctx, a.RoomID, before.Revision); err != nil {
		t.Fatal(err)
	}
	p, err := e.m.rooms().SessionPause(a.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	if !p.ResumePending {
		t.Fatal("resume recovery cleared before queued runner accepted its prompt; a killed worker is now stranded")
	}
}
