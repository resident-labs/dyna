package view_test

import (
	"dyna-agent/internal/room"
	roomfixture "dyna-agent/internal/room/testfixture"
	"testing"
)

func workspaceEnv(t *testing.T) (*env, room.Record, string) {
	t.Helper()
	e := newEnv(t)
	root := t.TempDir()
	rec, err := e.svc.Notify.Rooms.Create(room.CreateInput{Name: "workspace", Repo: root, CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	rec = roomfixture.Repository(t, e.svc.Notify.Rooms, rec.ID, "github.com", "acme/widgets", "read-write")
	return e, rec, root
}
