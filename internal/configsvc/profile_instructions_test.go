package configsvc_test

import (
	"context"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/configsvc"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
	"os"
	"strings"
	"testing"
	"time"
)

type heldProfileTurn struct{ lock *os.File }

func (s *heldProfileTurn) Spawn(_ *subagent.Manager, _ string, _ int, lock *os.File) error {
	s.lock = lock
	return nil
}

func TestProfileEditUsesInstructionChangeNotice(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	profiles := &profile.Store{Path: profile.DefaultPath(), Profiles: []profile.Profile{
		{Name: "mock", Harness: profile.HarnessMock, Default: true, Taste: 5, Intelligence: 5, Cost: 5},
		{Name: "writer", Harness: profile.HarnessMock, Model: "model-2", Taste: 7, Intelligence: 8, Cost: 6},
	}}
	if err := profiles.Save(); err != nil {
		t.Fatal(err)
	}
	m := subagent.NewManager()
	rec, err := m.RoomStore().Create(room.CreateInput{Name: "instructions"})
	if err != nil {
		t.Fatal(err)
	}
	held := &heldProfileTurn{}
	m.Spawner = held
	info, _, err := m.Start(context.Background(), subagent.StartOptions{Goal: "Check profile updates; done when the notice is stored", Room: rec.ID, Name: "reader", Profile: "mock", Dir: t.TempDir(), Task: "SLEEP:0\nRESPOND: ready"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.RunTurn(t.Context(), info.ID, 1, held.lock); err != nil {
		t.Fatal(err)
	}
	if err := (&configsvc.Service{}).SetProfileDisabled(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), "writer", true); err != nil {
		t.Fatal(err)
	}
	if err := (&configsvc.Service{}).SetProfileDisabled(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), "writer", true); err != nil {
		t.Fatal(err)
	}
	if _, err := m.FlushInstructionNotes(t.Context(), time.Now().Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	messages, err := m.RoomStore().Messages(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	notes := []room.Message{}
	for _, msg := range messages {
		if msg.Kind == "session" && msg.Notice.Kind == "instructions" {
			notes = append(notes, msg)
		}
	}
	if len(notes) != 1 || notes[0].Wake || !strings.Contains(notes[0].Text, "[DYNA INSTRUCTIONS CHANGED]") || !strings.Contains(notes[0].Text, "writer") {
		t.Fatalf("profile change notices: %+v", notes)
	}
	after, err := m.Store.ReadMeta(info.ID)
	if err != nil || after.Turns != 1 {
		t.Fatalf("profile edit woke the session: %+v,%v", after, err)
	}
}
