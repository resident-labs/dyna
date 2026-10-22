package subagent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/harness"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/state"
)

func accountSwitchNotes(t *testing.T, m *Manager, roomID string) []room.Message {
	t.Helper()
	msgs, err := m.rooms().Messages(roomID)
	if err != nil {
		t.Fatal(err)
	}
	var notes []room.Message
	for _, msg := range msgs {
		if msg.Kind == "account" {
			notes = append(notes, msg)
		}
	}
	return notes
}

func TestAccountSwitchPostsOneQuietLineToRoot(t *testing.T) {
	e := roomControlEnv(t)
	info := completedFixtureSession(t, e)
	meta, err := e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Dir(e.m.Store.root())
	if err := state.MigrateRequests(data); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	w := state.For(db)
	observed, notesAtCommit := false, 0
	var observationErr error
	w.Now = func() time.Time {
		if !observed {
			turn, err := e.m.Store.ReadTurn(info.ID, 1)
			observationErr = err
			if err == nil && turn.AccountSwitch != nil {
				observed = true
				msgs, err := e.m.rooms().Messages(info.RoomID)
				observationErr = err
				for _, msg := range msgs {
					if msg.Kind == "account" {
						notesAtCommit++
					}
				}
			}
		}
		return time.Now()
	}
	t.Cleanup(func() { w.Now = nil })
	actor := authority.WithActor(e.ctx, authority.Actor{Role: authority.Session, Session: info.ID})
	id := info.ID + "/1/account"
	request := func() context.Context { return state.ForRequest(callspec.WithRequest(actor, id), "runner/"+id) }
	note := TurnNote{Turn: 1, AccountSwitch: &harness.AccountSwitch{From: "work", To: "personal"}}
	if err := e.m.RunnerTurnNote(request(), note); err != nil {
		t.Fatal(err)
	}
	if !observed || notesAtCommit != 0 || observationErr != nil {
		t.Fatalf("commit observed %v, notes before commit %d, error %v", observed, notesAtCommit, observationErr)
	}
	if err := e.m.RunnerTurnNote(request(), note); !errors.Is(err, state.ErrRepeated) {
		t.Fatalf("replayed account switch = %v, want ErrRepeated", err)
	}
	msgs := accountSwitchNotes(t, e.m, info.RoomID)
	for _, m := range msgs {
		if m.Wake || len(m.To) != 1 || m.To[0] != meta.Room+":root" || !strings.Contains(m.Text, "work") || !strings.Contains(m.Text, "personal") {
			t.Fatalf("note = %+v", m)
		}
	}
	if len(msgs) != 1 {
		t.Fatalf("%d account notes, want 1", len(msgs))
	}
}

// A real session turn: the mock reports a usage limit, the runner's harness
// call moves it to another account, and the turn and root record the switch.
func TestSessionTurnOnAUsageLimitRecordsTheAccountSwitch(t *testing.T) {
	old := harness.FailoverAccount
	harness.FailoverAccount = func(context.Context, profile.Profile, string, string, error) (*harness.AccountSwitch, error) {
		return &harness.AccountSwitch{From: "work", To: "personal"}, nil
	}
	t.Cleanup(func() { harness.FailoverAccount = old })
	e := newTestEnv(t)
	info := completedFixtureSession(t, e, "SLEEP:0\nUSAGE_LIMIT: resets in 3h\nRESPOND: complete")
	if _, err := e.m.Store.UpdateTurn(info.ID, 1, func(turn *Turn) {
		turn.Status = TurnQueued
		turn.Started, turn.Ended = time.Time{}, time.Time{}
	}); err != nil {
		t.Fatal(err)
	}
	lock, err := e.m.Store.tryTurnLock(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.m.RunTurn(e.ctx, info.ID, 1, lock); err != nil {
		t.Fatal(err)
	}
	turn, err := e.m.Store.ReadTurn(info.ID, 1)
	if err != nil || turn.Status != TurnOK || turn.AccountSwitch == nil || turn.AccountSwitch.To != "personal" {
		t.Fatalf("turn = %+v, err = %v", turn, err)
	}
	if notes := accountSwitchNotes(t, e.m, info.RoomID); len(notes) != 1 {
		t.Fatalf("%d account notes, want 1", len(notes))
	}
}

func TestFailedAccountSwitchWritePostsNothing(t *testing.T) {
	e := roomControlEnv(t)
	info := completedFixtureSession(t, e)
	actor := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: info.ID})
	ctx, cancel := context.WithCancel(actor)
	cancel()
	if err := e.m.RunnerTurnNote(ctx, TurnNote{Turn: 1, AccountSwitch: &harness.AccountSwitch{From: "work", To: "personal"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled switch write = %v", err)
	}
	turn, err := e.m.Store.ReadTurn(info.ID, 1)
	if err != nil || turn.AccountSwitch != nil || len(accountSwitchNotes(t, e.m, info.RoomID)) != 0 {
		t.Fatalf("failed write stored a switch or a notice: %+v, error %v", turn.AccountSwitch, err)
	}
}

func TestAccountSwitchPostFailureStaysBestEffortOnReplay(t *testing.T) {
	e := roomControlEnv(t)
	info := completedFixtureSession(t, e)
	data := filepath.Dir(e.m.Store.root())
	if err := state.MigrateRequests(data); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Store.UpdateMeta(info.ID, func(meta *Meta) { meta.RoomID = "room_20000101-000000-00000001" }); err != nil {
		t.Fatal(err)
	}
	actor := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: info.ID})
	id := info.ID + "/1/account"
	request := func() context.Context { return state.ForRequest(callspec.WithRequest(actor, id), "runner/"+id) }
	note := TurnNote{Turn: 1, AccountSwitch: &harness.AccountSwitch{From: "work", To: "personal"}}
	if err := e.m.RunnerTurnNote(request(), note); err != nil {
		t.Fatalf("a quiet post failure failed the committed note: %v", err)
	}
	turn, err := e.m.Store.ReadTurn(info.ID, 1)
	if err != nil || turn.AccountSwitch == nil || turn.AccountSwitch.To != "personal" {
		t.Fatalf("post failure lost the committed switch: %+v, error %v", turn.AccountSwitch, err)
	}
	if _, err := e.m.Store.UpdateMeta(info.ID, func(meta *Meta) { meta.RoomID = info.RoomID }); err != nil {
		t.Fatal(err)
	}
	if err := e.m.RunnerTurnNote(request(), note); !errors.Is(err, state.ErrRepeated) {
		t.Fatalf("replay = %v, want ErrRepeated", err)
	}
	if notes := accountSwitchNotes(t, e.m, info.RoomID); len(notes) != 0 {
		t.Fatalf("replay repaired a best-effort post: %+v", notes)
	}
}
