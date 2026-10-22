package filesvc_test

import (
	"dyna-agent/internal/filesvc"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
	"testing"
)

type env struct {
	t     *testing.T
	svc   *roomsvc.Service
	files *filesvc.Service
}

func newEnv(t *testing.T) *env {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	svc := roomsvc.New(notify.New(room.DefaultStore(), &subagent.Manager{Store: subagent.DefaultStore()}))
	return &env{t: t, svc: svc, files: &filesvc.Service{Rooms: svc.Notify.Rooms, SessionInRoom: svc.SessionInRoom, AttachmentBytes: svc.AttachmentBytes}}
}
func (e *env) room(name string) room.Record {
	e.t.Helper()
	rec, err := e.svc.Notify.Rooms.Create(room.CreateInput{Name: name, CreatedBy: "test"})
	if err != nil {
		e.t.Fatal(err)
	}
	return rec
}
func (e *env) start(rec room.Record, name string, _ *subagent.Info) subagent.Meta {
	e.t.Helper()
	meta := subagent.Meta{ID: subagent.NewID(), Name: name, RoomID: rec.ID, Room: rec.Name, Dir: e.t.TempDir()}
	recordtest.Write(e.t, e.svc.Notify.Sessions.Store.Root, meta.ID, meta)
	return meta
}
