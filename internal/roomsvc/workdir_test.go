package roomsvc

import (
	"dyna-agent/internal/terminalsvc"
	"strings"
	"testing"
)

func TestRoomNeedsRepository(t *testing.T) {
	e := newEnv(t)
	t.Setenv("HOME", t.TempDir())
	rec := e.room("bare")
	if _, err := e.svc.Send(e.ctx, rec.ID, "/mock RESPOND: ready", "", ""); err == nil || !strings.Contains(err.Error(), "room bare has no repo") {
		t.Fatalf("bare spawn: %v", err)
	}
	if _, err := e.svc.files().WorkspaceRoot(rec.ID, ""); err == nil || err.Error() != "this room has no repository directory" {
		t.Fatalf("bare workspace: %v", err)
	}
	terminals := terminalsvc.New(terminalsvc.Options{Rooms: e.svc.rooms(), SessionRoot: e.svc.sessions().Root,
		SessionDirectory: func(ref string) (string, string, string, error) {
			meta, err := e.svc.sessions().ReadMeta(ref)
			return meta.RoomID, meta.ID, meta.Dir, err
		}})
	t.Cleanup(terminals.Shutdown)
	if _, _, _, err := terminals.TerminalDirectory(rec.ID); err == nil || err.Error() != "this room has no repository" {
		t.Fatalf("bare terminal: %v", err)
	}
	if members, err := e.svc.Members(rec.ID, true); err != nil || len(members) != 1 {
		t.Fatalf("bare members: %+v %v", members, err)
	}
}
