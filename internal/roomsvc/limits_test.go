package roomsvc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/room"
)

// A room's limits round trip through the service, and only the owner and the
// room's root may change them, the rule every room setting follows.
func TestSetRoomLimitRoundTripsAndGuardsTheCaller(t *testing.T) {
	e := newEnv(t)
	cozy := e.room("cozy")
	other := e.room("other")
	res, err := e.svc.SetRoomLimit(e.ctx, "cozy", "memoryHigh", "2G")
	if err != nil || res.ApplyWarning != "" {
		t.Fatalf("owner set memoryHigh: %v, %+v", err, res)
	}
	if _, err := e.svc.SetRoomLimit(e.ctx, cozy.ID, "memoryMax", "off"); err != nil {
		t.Fatalf("owner set memoryMax: %v", err)
	}
	if _, err := e.svc.SetRoomLimit(e.ctx, "cozy", "tasksMax", "512"); err != nil {
		t.Fatalf("owner set tasksMax: %v", err)
	}
	rec, err := e.svc.Notify.Rooms.Resolve("cozy")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Limits.MemoryHigh.Off || rec.Limits.MemoryHigh.Value != 2<<30 {
		t.Fatalf("memoryHigh = %+v, want 2G", rec.Limits.MemoryHigh)
	}
	if !rec.Limits.MemoryMax.Off || rec.Limits.MemoryMax.Value != 0 {
		t.Fatalf("memoryMax = %+v, want off", rec.Limits.MemoryMax)
	}
	if rec.Limits.TasksMax.Value != 512 {
		t.Fatalf("tasksMax = %+v, want 512", rec.Limits.TasksMax)
	}
	if got := room.LimitMemoryHigh.Format(rec.Limits.MemoryHigh); got != "2G" {
		t.Fatalf("memoryHigh shows %q, want 2G", got)
	}
	if _, err := e.svc.SetRoomLimit(e.ctx, "cozy", "memoryHigh", "default"); err != nil {
		t.Fatal(err)
	}
	if rec, err = e.svc.Notify.Rooms.Resolve("cozy"); err != nil || rec.Limits.MemoryHigh != (room.Limit{}) {
		t.Fatalf("default did not clear the limit: %+v, %v", rec.Limits.MemoryHigh, err)
	}

	// A session may not change a room setting, and a root stays in its own room.
	session := authority.WithActor(e.ctx, authority.Actor{Role: authority.Session, Session: "ses_1"})
	if _, err := e.svc.SetRoomLimit(session, "cozy", "memoryHigh", "1G"); err == nil || !strings.Contains(err.Error(), "owner or root only") {
		t.Fatalf("session set err = %v", err)
	}
	stranger := authority.WithActor(e.ctx, authority.Actor{Role: authority.Root, Room: other.ID})
	if _, err := e.svc.SetRoomLimit(stranger, "cozy", "memoryHigh", "1G"); err == nil || !strings.Contains(err.Error(), "own room") {
		t.Fatalf("other room's root set err = %v", err)
	}
	root := authority.WithActor(e.ctx, authority.Actor{Role: authority.Root, Room: cozy.ID})
	if _, err := e.svc.SetRoomLimit(root, "cozy", "memoryHigh", "1G"); err != nil {
		t.Fatalf("the room's root set: %v", err)
	}

	// Values are checked before they are stored.
	for _, bad := range []struct{ field, value string }{
		{"memoryHigh", "soon"}, {"memoryHigh", "0"}, {"tasksMax", "2G"}, {"tasksMax", "-5"}, {"swapMax", "1G"},
	} {
		if _, err := e.svc.SetRoomLimit(e.ctx, "cozy", bad.field, bad.value); err == nil {
			t.Fatalf("set %s %q was accepted", bad.field, bad.value)
		}
	}
}

// A failed live apply does not lose the saved limit: the call reports a
// warning instead of an error, so the clients keep what the room now stores.
// A configuration directory that is a file makes the apply fail after the
// store write, without touching systemd.
func TestSetRoomLimitKeepsTheSavedValueWhenTheLiveApplyFails(t *testing.T) {
	e := newEnv(t)
	cozy := e.room("cozy")
	notADir := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(notADir, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", notADir)
	res, err := e.svc.SetRoomLimit(e.ctx, "cozy", "memoryHigh", "2G")
	if err != nil {
		t.Fatalf("a failed live apply returned an error: %v", err)
	}
	if res.ApplyWarning == "" {
		t.Fatal("the failed live apply produced no warning")
	}
	rec, err := e.svc.Notify.Rooms.Resolve(cozy.Name)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Limits.MemoryHigh.Off || rec.Limits.MemoryHigh.Value != 2<<30 {
		t.Fatalf("the saved limit was lost: %+v", rec.Limits.MemoryHigh)
	}
}
