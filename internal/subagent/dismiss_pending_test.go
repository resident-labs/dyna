package subagent

import (
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/mode"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store/storetest"
)

func ownerDismissEnv(t *testing.T) {
	t.Helper()
	storetest.ClearStamp(t)
}

func TestOwnerDismissPendingFromAnySender(t *testing.T) {
	for _, state := range []string{"waiting", "claimed", "inherited"} {
		t.Run(state, func(t *testing.T) {
			e := roomControlEnv(t)
			info := completedFixtureSession(t, e)
			source := info
			if state == "inherited" {
				source = completedFixtureSession(t, e)
			}
			msg, err := e.m.rooms().Post(info.RoomID, room.PostInput{Wake: true, From: info.Room + ":reporter", To: []string{source.Address()}, Text: "pending agent report"})
			if err != nil {
				t.Fatal(err)
			}
			if state == "claimed" {
				dir, _ := e.m.Store.Dir(info.ID)
				q, err := readQueue(dir)
				if err != nil {
					t.Fatal(err)
				}
				q.Claimed[msg.ID] = 1
				if err := writeQueue(dir, q); err != nil {
					t.Fatal(err)
				}
				if _, err := e.m.Store.UpdateTurn(info.ID, 1, func(turn *Turn) { turn.Steering = append(turn.Steering, Steer{ID: msg.ID}) }); err != nil {
					t.Fatal(err)
				}
			}
			if state == "inherited" {
				if _, err := e.m.Store.UpdateMeta(info.ID, func(meta *Meta) {
					meta.InheritedInboxes = []InheritedInbox{{Session: source.ID, Address: source.Address(), PendingIDs: []string{msg.ID}}}
				}); err != nil {
					t.Fatal(err)
				}
			}
			ownerDismissEnv(t)
			if err := e.m.DismissPendingPost(e.ctx, info.ID, msg.ID, false); err == nil {
				t.Fatal("dismissed without confirmation")
			}
			if err := e.m.DismissPendingPost(e.ctx, info.ID, msg.ID, true); err != nil {
				t.Fatal(err)
			}
			if err := e.m.DismissPendingPost(e.ctx, info.ID, msg.ID, true); err != nil {
				t.Fatal("dismiss not idempotent", err)
			}
			if _, err := e.m.rooms().Message(info.RoomID, msg.ID); err != nil {
				t.Fatal("history removed", err)
			}
			queue, err := e.m.Store.SessionQueue(e.m.rooms(), info.ID)
			if err != nil || len(queue) != 1 || queue[0].State != "dismissed" {
				t.Fatalf("queue: %+v %v", queue, err)
			}
			if err := e.m.Store.writeTurn(info.ID, Turn{N: 2, Status: TurnOK, Created: time.Now(), Ended: time.Now()}); err != nil {
				t.Fatal(err)
			}
			meta, err := e.m.Store.UpdateMeta(info.ID, func(meta *Meta) { meta.Turns = 2 })
			if err != nil {
				t.Fatal(err)
			}
			msgs, _, err := e.m.drainRoomMessages(meta, Turn{N: 2})
			if err != nil || len(msgs) != 0 {
				t.Fatalf("dismissed post redelivered: %+v %v", msgs, err)
			}
			gate := e.m.Store.newTurnGate(e.m.rooms(), meta, 2, meta.RoomCursor)
			if err := gate.Activate(); err != nil {
				t.Fatal(err)
			}
			steers, _, err := gate.Read(0, false)
			if err != nil || len(steers) != 0 {
				t.Fatalf("dismissed post steered: %+v %v", steers, err)
			}
			if _, err := e.m.Store.UpdateMeta(info.ID, func(meta *Meta) {
				meta.LastActivity, meta.Finished = time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)
				meta.Status = StatusDormant
			}); err != nil {
				t.Fatal(err)
			}
			e.doneGoals()
			result, err := closeIdle(e.m, e.ctx, info.RoomID)
			if err != nil {
				t.Fatal(err)
			}
			meta, err = e.m.Store.ReadMeta(info.ID)
			if err != nil || meta.Closed.IsZero() {
				t.Fatalf("dismissed post blocked dormant close: %+v %v", result, err)
			}
		})
	}
}

func TestDismissPendingRejectsWorkersAndActiveDelivery(t *testing.T) {
	e := roomControlEnv(t)
	info := completedFixtureSession(t, e, "SLEEP:1 RESPOND: idle")
	msg, err := e.m.rooms().Post(info.RoomID, room.PostInput{Wake: true, From: info.Room + ":reporter", To: []string{info.Address()}, Text: "keep"})
	if err != nil {
		t.Fatal(err)
	}
	ownerDismissEnv(t)
	for _, env := range []struct{ key, value string }{{mode.EnvMode, mode.Workflow}, {mode.EnvMode, mode.Subagent}, {mode.EnvSession, info.ID}, {mode.EnvWorkerRun, "run_worker"}} {
		t.Run(env.key+env.value, func(t *testing.T) {
			t.Setenv(env.key, env.value)
			if err := e.m.DismissPendingPost(e.ctx, info.ID, msg.ID, true); err == nil {
				t.Fatal("worker dismissed a post")
			}
		})
	}
	lock, err := e.m.Store.tryTurnLock(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	err = e.m.DismissPendingPost(e.ctx, info.ID, msg.ID, true)
	lock.Close()
	if err == nil || !strings.Contains(err.Error(), "active delivery") {
		t.Fatalf("active delivery was not protected: %v", err)
	}
	meta, err := e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := e.m.pendingRoomMessages(meta)
	if err != nil || len(pending) != 1 {
		t.Fatalf("rejected dismissal changed pending state: %+v %v", pending, err)
	}
	if err := e.m.DismissPendingPost(e.ctx, info.ID, "msg_0000000000000000", true); err == nil {
		t.Fatal("accepted unknown post")
	}
	// The internal root acts as the owner, although Dyna stamps it.
	rootModeEnv(t, info.RoomID)
	if err := e.m.DismissPendingPost(e.ctx, info.ID, msg.ID, true); err != nil {
		t.Fatal(err)
	}
}

// rootModeEnv stamps the process the way Dyna stamps the internal root.
func rootModeEnv(t *testing.T, roomID string) {
	t.Helper()
	t.Setenv(mode.EnvMode, mode.Root)
	t.Setenv(mode.EnvSession, "ses_20260101-120000-abcdef12")
	t.Setenv(mode.EnvRoomID, roomID)
}

func TestDismissPendingRejectsSavedHeldPromptWithoutLiveLock(t *testing.T) {
	e := roomControlEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: idle")
	info, _ = e.wait(info.ID, 1)
	msg, err := e.m.rooms().Post(info.RoomID, room.PostInput{Wake: true, From: info.Room + ":reporter", To: []string{info.Address()}, Text: "saved input"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Store.UpdateTurn(info.ID, 1, func(turn *Turn) {
		turn.Status = TurnQueued
		turn.RoomHeld = true
		turn.HeldPrompt = msg.Text
		turn.HeldAccepted = false
	}); err != nil {
		t.Fatal(err)
	}
	ownerDismissEnv(t)
	// A held queued turn has no live lock, but its saved prompt owns the input.
	lock, err := e.m.Store.tryTurnLock(info.ID)
	if err != nil {
		t.Fatal("held prompt unexpectedly had a live turn lock", err)
	}
	lock.Close()
	if err := e.m.DismissPendingPost(e.ctx, info.ID, msg.ID, true); err == nil || !strings.Contains(err.Error(), "unfinished or held turn") {
		t.Fatalf("accepted saved input dismissal: %v", err)
	}
	dir, _ := e.m.Store.Dir(info.ID)
	q, err := readQueue(dir)
	if err != nil || q.Dismissed[msg.ID] {
		t.Fatalf("rejected dismissal changed queue: %+v %v", q, err)
	}
	turn, err := e.m.Store.ReadTurn(info.ID, 1)
	if err != nil || turn.HeldPrompt != msg.Text {
		t.Fatalf("saved input changed: %+v %v", turn, err)
	}
}
