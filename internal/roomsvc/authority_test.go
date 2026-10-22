package roomsvc

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/store/storetest"
)

func TestRoomAuthorityBeforeBackend(t *testing.T) {
	t.Setenv(mode.EnvMode, mode.Subagent)
	t.Setenv(mode.EnvSession, "session")
	svc := &Service{}
	_, err := svc.CreateRoom(context.Background(), CreateRoomInput{Path: "/missing"})
	if err == nil || !strings.HasPrefix(err.Error(), "room create is owner or root only;") {
		t.Fatalf("create: %v", err)
	}
	err = svc.DeleteRoom(context.Background(), "missing")
	if err == nil || !strings.HasPrefix(err.Error(), "room delete is owner or root only;") {
		t.Fatalf("delete: %v", err)
	}
	_, err = svc.AttachSeat(context.Background(), "missing", "")
	if err == nil || !strings.HasPrefix(err.Error(), "root attach is owner only;") {
		t.Fatalf("attach: %v", err)
	}
	_, err = svc.ReturnSeat(context.Background(), "missing", "")
	if err == nil || !strings.HasPrefix(err.Error(), "root detach is owner only;") {
		t.Fatalf("detach: %v", err)
	}
}

// The engine runs as the owner, so a call it serves checks the caller's actor
// from the call's context, never the engine process's own (requirements
// item 3).
func TestRoomAuthorityUsesTheCallersActor(t *testing.T) {
	storetest.ClearStamp(t)
	ctx := authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Session: "session"})
	svc := &Service{}
	if _, err := svc.CreateRoom(ctx, CreateRoomInput{Path: "/missing"}); err == nil || !strings.HasPrefix(err.Error(), "room create is owner or root only;") {
		t.Fatalf("create: %v", err)
	}
	if err := svc.DeleteRoom(ctx, "missing"); err == nil || !strings.HasPrefix(err.Error(), "room delete is owner or root only;") {
		t.Fatalf("delete: %v", err)
	}
	if _, err := svc.AttachSeat(ctx, "missing", ""); err == nil || !strings.HasPrefix(err.Error(), "root attach is owner only;") {
		t.Fatalf("attach: %v", err)
	}
	if _, err := svc.ReturnSeat(ctx, "missing", ""); err == nil || !strings.HasPrefix(err.Error(), "root detach is owner only;") {
		t.Fatalf("detach: %v", err)
	}
}
