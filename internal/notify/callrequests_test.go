package notify

import (
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/subagent"
)

func callTestService(t *testing.T) (*Service, room.Record) {
	t.Helper()
	root := t.TempDir()
	s := New(&room.Store{Root: filepath.Join(root, "rooms")}, &subagent.Manager{Store: &subagent.Store{Root: filepath.Join(root, "sessions")}})
	rec, err := s.Rooms.Create(room.CreateInput{Name: "calls"})
	if err != nil {
		t.Fatal(err)
	}
	return s, rec
}

func TestCallRootAuthority(t *testing.T) {
	s, rec := callTestService(t)
	meta := subagent.Meta{ID: "ses_20261020-000000-abcdef12", Room: rec.Name, RoomID: rec.ID}
	if err := sessionlife.Save(s.sessionStore().Root, meta.ID, meta); err != nil {
		t.Fatal(err)
	}
	// A caller-supplied root role must not override the stored ordinary session.
	forged := Sender{Kind: SenderSession, Room: rec.Name, Session: subagent.Info{Meta: subagent.Meta{ID: meta.ID, Role: subagent.RoleRoot}}}
	for _, snd := range []Sender{{Kind: SenderOwner, Human: true}, {Kind: SenderScript}, {Kind: SenderVoice}, forged} {
		if _, err := s.RequestCall(snd, "", "Urgent decision"); err == nil || !strings.Contains(err.Error(), "root only") {
			t.Fatalf("request sender %v: %v", snd.Kind, err)
		}
		if _, err := s.CallRoom(snd, ""); err == nil {
			t.Fatalf("list allowed sender %v", snd.Kind)
		}
		if err := s.WithdrawCall(snd, "", "c1"); err == nil {
			t.Fatalf("withdraw allowed sender %v", snd.Kind)
		}
	}
}

func TestCallReasonAndWithdraw(t *testing.T) {
	s, rec := callTestService(t)
	snd := Sender{Kind: SenderOwner, Room: rec.Name}
	if s.CallRequestStore().Root != filepath.Dir(s.sessionStore().Root) {
		t.Fatal("call store root does not match session database")
	}
	for _, reason := range []string{"", " \n "} {
		if _, err := s.RequestCall(snd, "", reason); err == nil || !strings.Contains(err.Error(), "--reason") {
			t.Fatalf("reason %q: %v", reason, err)
		}
	}
	req, err := s.RequestCall(snd, "", "  Owner must sign in  ")
	if err != nil {
		t.Fatal(err)
	}
	if req.Reason != "Owner must sign in" || req.Asker != "root" || req.Room != rec.ID || req.State != "ringing" {
		t.Fatalf("request: %+v", req)
	}
	duplicate, err := s.RequestCall(snd, "", "Another reason")
	if err != nil || duplicate.ID != req.ID {
		t.Fatalf("duplicate request: %+v, %v", duplicate, err)
	}
	if err := s.WithdrawCall(snd, "", req.ID); err != nil {
		t.Fatal(err)
	}
	closed, err := s.CallRequestStore().Get(rec.ID, req.ID)
	if err != nil || closed.State != "withdrawn" || closed.ClosedBy != "root" {
		t.Fatalf("withdrawn: %+v, %v", closed, err)
	}
	other, err := s.Rooms.Create(room.CreateInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CallRoom(snd, other.ID); err == nil {
		t.Fatal("allowed request outside sender room")
	}
}

func TestCallStoredRootRoom(t *testing.T) {
	s, rec := callTestService(t)
	meta := subagent.Meta{ID: "ses_20261020-000000-abcdef13", Room: rec.Name, RoomID: rec.ID, Role: subagent.RoleRoot, Service: true}
	if err := sessionlife.Save(s.sessionStore().Root, meta.ID, meta); err != nil {
		t.Fatal(err)
	}
	snd := Sender{Kind: SenderSession, Room: "forged-room", Session: subagent.Info{Meta: subagent.Meta{ID: meta.ID}}}
	req, err := s.RequestCall(snd, "", "Owner action")
	if err != nil || req.Room != rec.ID || req.Asker != meta.ID {
		t.Fatalf("stored root request: %+v, %v", req, err)
	}
}
