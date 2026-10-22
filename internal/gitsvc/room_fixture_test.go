package gitsvc_test

import (
	"dyna-agent/internal/filesvc"
	"dyna-agent/internal/gitsvc"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
	"os"
	"path/filepath"
	"testing"
)

type env struct {
	t     *testing.T
	svc   *roomsvc.Service
	git   *gitsvc.Service
	files *filesvc.Service
}

func workspaceEnv(t *testing.T) (*env, room.Record, string) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	svc := roomsvc.New(nil)
	root := t.TempDir()
	rec, err := svc.Notify.Rooms.Create(room.CreateInput{Name: "workspace", Repo: root, CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	files := &filesvc.Service{Rooms: svc.Notify.Rooms, SessionInRoom: svc.SessionInRoom, AttachmentBytes: svc.AttachmentBytes}
	return &env{t: t, svc: svc, files: files, git: &gitsvc.Service{WorkspaceRoot: files.WorkspaceRoot, MaxBytes: filesvc.MaxWorkspaceBytes}}, rec, root
}
func writeWorkspace(t *testing.T, root, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func (e *env) start(rec room.Record, name string) subagent.Meta {
	e.t.Helper()
	meta := subagent.Meta{ID: subagent.NewID(), Name: name, RoomID: rec.ID, Room: rec.Name, Dir: e.t.TempDir()}
	recordtest.Write(e.t, e.svc.Notify.Sessions.Store.Root, meta.ID, meta)
	return meta
}
