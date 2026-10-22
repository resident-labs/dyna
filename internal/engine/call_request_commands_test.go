package engine

import (
	"os/exec"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callrequests"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/store"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

// Caller identity comes from the request context, even when the engine
// environment claims a different session. Stored roles and rooms still apply.
func TestCallSenderUsesCallerAndStoredRoot(t *testing.T) {
	s := testServices(t, false)
	rec, err := s.rooms.Notify.Rooms.Create(room.CreateInput{Name: "calls"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.rooms.Notify.Rooms.Create(room.CreateInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	root := subagent.Meta{ID: "ses_20261020-000000-abcdef14", Room: rec.Name, RoomID: rec.ID, Role: subagent.RoleRoot}
	ordinary := subagent.Meta{ID: "ses_20261020-000000-abcdef15", Room: rec.Name, RoomID: rec.ID}
	for _, meta := range []subagent.Meta{root, ordinary} {
		if err := sessionlife.Save(s.rooms.Notify.Sessions.Store.Root, meta.ID, meta); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(mode.EnvMode, mode.Subagent)
	t.Setenv(mode.EnvSession, ordinary.ID)
	sender := func(actor authority.Actor) (notify.Sender, error) {
		caller := websvc.Caller{Kind: websvc.Unverified}
		if actor.Session != "" {
			caller = websvc.Caller{Kind: websvc.Session, Session: actor.Session}
		}
		if actor.Role == authority.Worker {
			caller = websvc.Caller{Kind: websvc.Worker, Run: actor.Run}
		}
		return s.callSenderFor(caller, true, actor)
	}
	snd, err := sender(authority.Actor{Role: authority.Root, Session: root.ID})
	if err != nil {
		t.Fatal(err)
	}
	req, err := s.rooms.Notify.RequestCall(snd, "", "Owner action")
	if err != nil || req.Asker != root.ID || req.Room != rec.ID {
		t.Fatalf("root request: %+v, %v", req, err)
	}
	for _, actor := range []authority.Actor{{}, {Role: authority.Worker, Run: "wf_1", Agent: 1}, {Role: authority.Session, Session: ordinary.ID}, {Role: authority.Root, Session: ordinary.ID}} {
		denied, err := sender(actor)
		if err != nil {
			continue
		}
		if _, err := s.rooms.Notify.RequestCall(denied, rec.ID, "Owner action"); err == nil {
			t.Errorf("request allowed %+v", actor)
		}
		if err := s.rooms.Notify.WithdrawCall(denied, rec.ID, req.ID); err == nil {
			t.Errorf("withdraw allowed %+v", actor)
		}
	}
	if _, err := s.rooms.Notify.RequestCall(snd, other.ID, "Owner action"); err == nil {
		t.Fatal("root requested a call outside its stored room")
	}
	if err := s.rooms.Notify.WithdrawCall(snd, other.ID, req.ID); err == nil {
		t.Fatal("root withdrew a call outside its stored room")
	}
	if err := s.rooms.Notify.WithdrawCall(snd, rec.ID, req.ID); err != nil {
		t.Fatal(err)
	}
	saved, err := s.rooms.Notify.CallRequestStore().Get(rec.ID, req.ID)
	if err != nil || saved.State != "withdrawn" || saved.ClosedBy != root.ID {
		t.Fatalf("withdraw: %+v, %v", saved, err)
	}
}

// The socket bindings run both writes in the engine process. A read-only
// engine refuses the writes before either handler runs.
func TestCallCommandsOverEngineSocket(t *testing.T) {
	s := testServices(t, false)
	rec, err := s.rooms.Notify.Rooms.Create(room.CreateInput{Name: "calls"})
	if err != nil {
		t.Fatal(err)
	}
	client := callCommandClient(t, s)
	var req callrequests.Request
	if err := client.Call(t.Context(), "RequestCall", []any{rec.ID, "", "Owner action"}, &req); err != nil {
		t.Fatal(err)
	}
	if req.ID != "c1" || req.Asker != "root" {
		t.Fatalf("request: %+v", req)
	}
	if err := client.Call(t.Context(), "WithdrawCall", []any{rec.ID, "", req.ID}, nil); err != nil {
		t.Fatal(err)
	}
	row, err := s.rooms.Notify.CallRequestStore().Get(rec.ID, req.ID)
	if err != nil || row.State != "withdrawn" {
		t.Fatalf("withdraw: %+v, %v", row, err)
	}
	readOnly, _, err := callsFor(s, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"RequestCall", "WithdrawCall"} {
		if _, err := readOnly[name](t.Context(), nil); err == nil {
			t.Errorf("read-only engine allowed %s", name)
		}
	}
}

func TestCallDefaultRoomUsesCallerWorkingDirectory(t *testing.T) {
	s := testServices(t, false)
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s, %v", out, err)
	}
	client := callCommandClient(t, s)
	var req callrequests.Request
	err := client.Call(t.Context(), "RequestCall", []any{"", dir, "Owner action"}, &req)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := s.rooms.Notify.Rooms.Resolve(req.Room)
	if err != nil || rec.Repo != dir {
		t.Fatalf("caller repo: %+v, %v", rec, err)
	}
	if err := client.Call(t.Context(), "WithdrawCall", []any{"", dir, req.ID}, nil); err != nil {
		t.Fatal(err)
	}
}

func callCommandClient(t *testing.T, s services) *engineclient.Client {
	t.Helper()
	calls, _, err := callsFor(s, false)
	if err != nil {
		t.Fatal(err)
	}
	callers := socketCallers{sessions: s.rooms.Notify.Sessions.Store}
	callers.actForCallers(calls)
	return engineclient.New(enginetest.Serve(t, store.DataDir(), calls))
}

func TestCallCommandsRefuseUnverifiedAndRemoteCallers(t *testing.T) {
	s := testServices(t, false)
	owner := authority.Actor{Role: authority.Owner}
	for _, caller := range []websvc.Caller{{Kind: websvc.Unverified}, {Kind: websvc.Session, Session: "ses_unknown"}} {
		if snd, err := s.callSenderFor(caller, true, owner); err == nil {
			t.Fatalf("unverified caller became %+v", snd)
		}
	}
	ctx := authority.WithActor(t.Context(), owner)
	if _, err := s.requestCall(ctx, "calls", "/host/path", "Owner action"); err == nil {
		t.Fatal("remote caller requested a call")
	}
	if err := s.withdrawCall(ctx, "calls", "/host/path", "c1"); err == nil {
		t.Fatal("remote caller withdrew a call")
	}
	rows, err := s.rooms.Notify.CallRequestStore().List()
	if err != nil || len(rows) != 0 {
		t.Fatalf("refused caller wrote requests: %+v, %v", rows, err)
	}
}
