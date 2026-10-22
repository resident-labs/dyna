package goalkeeper

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/subagent"
)

const (
	leadID  = "ses_20261019-000001-11111111"
	busyID  = "ses_20261019-000002-22222222"
	childID = "ses_20261019-000003-33333333"
	svcID   = "ses_20261019-000004-44444444"
	rootID  = "ses_20261019-000005-55555555"
)

type checkInEnv struct {
	*escalationEnv
	f   *fakeSessions
	now time.Time
	v   settings.Values
}

// newCheckInEnv has an idle top-level lead, a running top-level agent, the
// lead's child, a service session and root. Each holds one goal, and an idle
// keeper session is open.
func newCheckInEnv(t *testing.T) *checkInEnv {
	e := &checkInEnv{escalationEnv: newEscalationEnv(t), now: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC), v: settings.Defaults()}
	e.s.Now = func() time.Time { return e.now }
	e.s.Store.Now = e.s.Now
	e.s.PRState = func() (state.State, error) { return state.State{}, nil }
	e.meta(subagent.Meta{ID: leadID, Name: "lead"}, subagent.StatusIdle)
	e.meta(subagent.Meta{ID: busyID, Name: "busy"}, subagent.StatusRunning)
	e.meta(subagent.Meta{ID: childID, Name: "child", ParentSession: leadID}, subagent.StatusIdle)
	e.meta(subagent.Meta{ID: svcID, Name: "svc", Service: true, Role: "reviewer"}, subagent.StatusIdle)
	e.meta(subagent.Meta{ID: rootID, Name: "root", Role: subagent.RoleRoot}, subagent.StatusIdle)
	for _, id := range []string{leadID, busyID, childID, svcID, rootID} {
		if _, err := e.s.Store.Add(e.rec.ID, "owner", goals.Input{Text: "Goal of " + id, Holders: []string{id}}); err != nil {
			t.Fatal(err)
		}
	}
	e.f = &fakeSessions{
		info: subagent.Info{Meta: subagent.Meta{ID: "keeper-session", Role: subagent.RoleKeeper, Name: "keeper", RoomID: e.rec.ID, Room: e.rec.Name, Profile: "best", Turns: 1, Resumable: true, HarnessSession: "h"}, Status: subagent.StatusIdle},
		turn: subagent.Turn{N: 1, Status: subagent.TurnOK},
	}
	e.s.Sessions = e.f
	return e
}

func (e *checkInEnv) meta(meta subagent.Meta, status string) {
	e.t.Helper()
	meta.Room, meta.RoomID, meta.Profile, meta.Resumable, meta.HarnessSession, meta.Status = e.rec.Name, e.rec.ID, "mock", true, "h-"+meta.ID, status
	dir := filepath.Join(e.mgr.Store.Root, meta.ID)
	if err := os.MkdirAll(filepath.Join(dir, "turns"), 0700); err != nil {
		e.t.Fatal(err)
	}
	turn := subagent.Turn{N: 1, Status: subagent.TurnOK, Created: e.now, Started: e.now, Ended: e.now}
	if status == subagent.StatusRunning {
		turn.Status, turn.Ended = subagent.TurnRunning, time.Time{}
	}
	meta.Turns = 1
	recordtest.Write(e.t, e.mgr.Store.Root, meta.ID, meta)
	b, _ := json.Marshal(turn)
	if err := os.WriteFile(filepath.Join(dir, "turns", "1.json"), b, 0600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *checkInEnv) tick(after time.Duration) {
	e.t.Helper()
	e.now = e.now.Add(after)
	if _, err := e.s.Tick(context.Background()); err != nil {
		e.t.Fatal(err)
	}
}

func (e *checkInEnv) posts(from string) []room.Message {
	e.t.Helper()
	msgs, err := e.rooms.Messages(e.rec.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []room.Message
	for _, m := range msgs {
		if m.From == from {
			out = append(out, m)
		}
	}
	return out
}

// alerts lists keeper failure alerts, both posted and still queued.
func (e *checkInEnv) alerts() []string {
	e.t.Helper()
	var out []string
	for _, m := range e.posts(room.GoalKeeperSender) {
		if strings.HasPrefix(m.Text, "Service turn produced no artifact") {
			out = append(out, m.Text)
		}
	}
	st, err := e.s.Store.Read()
	if err != nil {
		e.t.Fatal(err)
	}
	for _, n := range st.Room(e.rec.ID).Notices {
		if n.Kind == "alert" {
			out = append(out, n.Text)
		}
	}
	return out
}
