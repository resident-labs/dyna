package subagent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/room"
)

func TestImportLegacyForwardedPosts(t *testing.T) {
	e := newTestEnv(t)
	source := completedFixtureSession(t, e)
	db, err := database.Open(filepath.Dir(e.m.Store.root()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE session_records SET meta=json_set(meta,'$.forwardedPosts',json('{"msg_1":"msg_2"}')) WHERE id=?`, source.ID); err != nil {
		t.Fatal(err)
	}
	got, err := e.m.Store.forwardedPosts(source.ID)
	if err != nil || got["msg_1"] != "msg_2" {
		t.Fatalf("import = %v, %v", got, err)
	}
	if _, err := e.m.Store.UpdateMeta(source.ID, func(m *Meta) { m.Name = "renamed" }); err != nil {
		t.Fatal(err)
	}
	got, err = e.m.Store.forwardedPosts(source.ID)
	if err != nil || got["msg_1"] != "msg_2" {
		t.Fatalf("receipt after metadata rewrite = %v, %v", got, err)
	}
}

func TestForwardFailureStillFinishesCloseCleanup(t *testing.T) {
	e := newTestEnv(t)
	parent, source := completedFixtureSession(t, e), completedFixtureSession(t, e)
	if _, err := e.m.Store.UpdateMeta(source.ID, func(m *Meta) { m.ParentSession = parent.ID; m.Depth = 2 }); err != nil {
		t.Fatal(err)
	}
	_, err := e.m.rooms().Post(source.RoomID, room.PostInput{From: source.Room, To: []string{source.Address()}, Wake: true, Text: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	e.m.HarnessStopper = func(context.Context, string) error { calls++; return nil }
	e.m.DeliverClosedForward = func(context.Context, room.Message) error { return errors.New("injected delivery error") }
	_, err = e.m.Close(e.ctx, source.ID)
	if err == nil || !strings.Contains(err.Error(), "injected delivery error") {
		t.Fatalf("close error = %v", err)
	}
	got, err := e.m.Store.Info(source.ID)
	if err != nil || got.Closed.IsZero() || calls != 2 {
		t.Fatalf("close = %+v, cleanup calls %d, %v", got, calls, err)
	}
	if by := closedBy(t, e.m, source.ID); by == "" {
		t.Fatal("close journal was not written")
	}
}
