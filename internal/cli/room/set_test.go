package room

import (
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	dynastore "dyna-agent/internal/store"
	"dyna-agent/internal/websvc"
)

// serveRoomSet serves the engine calls the set command makes on the same room
// service the engine runs. This stand-in keeps the CLI's writer; the engine's
// authority rules are tested in internal/engine.
func serveRoomSet(t *testing.T) {
	t.Helper()
	svc := roomsvc.New(nil)
	calls, err := websvc.Bind([]websvc.Binding{
		{Name: "CreateRoomRecord", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: svc.CreateRoomRecord},
		{Name: "SetRoomCheckIn", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: svc.SetRoomCheckIn},
		{Name: "SetRoomLimit", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: svc.SetRoomLimit},
	})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, dynastore.DataDir(), calls)
}

func TestRoomSetCheckInIntervalSetsShowsAndClears(t *testing.T) {
	clearStamp(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	serveRoomSet(t)
	t.Chdir(t.TempDir())
	if _, err := execCmd(t, RoomCmd(), "create", "cozy"); err != nil {
		t.Fatal(err)
	}
	stored := func() (off bool, every time.Duration) {
		t.Helper()
		rec, err := store().Resolve("cozy")
		if err != nil {
			t.Fatal(err)
		}
		return rec.CheckIn.Off, rec.CheckIn.Every
	}
	if shown, err := execCmd(t, RoomCmd(), "set", "cozy", "checkInInterval"); err != nil || strings.TrimSpace(shown) != "default" {
		t.Fatalf("initial value = %q, %v", shown, err)
	}
	if out, err := execCmd(t, RoomCmd(), "set", "cozy", "checkInInterval", "2h"); err != nil || !strings.Contains(out, "2h") {
		t.Fatalf("set 2h = %q, %v", out, err)
	}
	if off, every := stored(); off || every != 2*time.Hour {
		t.Fatalf("stored setting = off %v every %v", off, every)
	}
	if _, err := execCmd(t, RoomCmd(), "set", "cozy", "checkInInterval", "off"); err != nil {
		t.Fatal(err)
	}
	if off, _ := stored(); !off {
		t.Fatal("off was not stored")
	}
	if shown, err := execCmd(t, RoomCmd(), "set", "cozy", "checkInInterval"); err != nil || strings.TrimSpace(shown) != "off" {
		t.Fatalf("shown off = %q, %v", shown, err)
	}
	if _, err := execCmd(t, RoomCmd(), "set", "cozy", "checkInInterval", "default"); err != nil {
		t.Fatal(err)
	}
	if off, every := stored(); off || every != 0 {
		t.Fatalf("default did not clear the setting: off %v every %v", off, every)
	}
	if _, err := execCmd(t, RoomCmd(), "set", "cozy", "checkInInterval", "30s"); err == nil || !strings.Contains(err.Error(), "at least 1m") {
		t.Fatalf("too-short interval err = %v", err)
	}
	if _, err := execCmd(t, RoomCmd(), "set", "cozy", "checkInInterval", "soon"); err == nil || !strings.Contains(err.Error(), "duration") {
		t.Fatalf("bad value err = %v", err)
	}
	if _, err := execCmd(t, RoomCmd(), "set", "cozy", "mergeMaxParallel", "3"); err == nil || !strings.Contains(err.Error(), "unknown room setting") {
		t.Fatalf("unknown key err = %v", err)
	}
}

// The limits settings set, show and clear through the same command, with the
// values the room's slice uses.
func TestRoomSetLimitsSetsShowsAndClears(t *testing.T) {
	clearStamp(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // the engine call rewrites the room's slice file
	serveRoomSet(t)
	t.Chdir(t.TempDir())
	if _, err := execCmd(t, RoomCmd(), "create", "cozy"); err != nil {
		t.Fatal(err)
	}
	stored := func(field room.LimitField) room.Limit {
		t.Helper()
		rec, err := store().Resolve("cozy")
		if err != nil {
			t.Fatal(err)
		}
		return rec.Limits.LimitOf(field)
	}
	if shown, err := execCmd(t, RoomCmd(), "set", "cozy", "limits.memoryHigh"); err != nil || strings.TrimSpace(shown) != "default" {
		t.Fatalf("initial value = %q, %v", shown, err)
	}
	if out, err := execCmd(t, RoomCmd(), "set", "cozy", "limits.memoryHigh", "2G"); err != nil || !strings.Contains(out, "2G") {
		t.Fatalf("set 2G = %q, %v", out, err)
	}
	if l := stored(room.LimitMemoryHigh); l.Off || l.Value != 2<<30 {
		t.Fatalf("stored memoryHigh = %+v", l)
	}
	if _, err := execCmd(t, RoomCmd(), "set", "cozy", "limits.tasksMax", "512"); err != nil {
		t.Fatal(err)
	}
	if shown, err := execCmd(t, RoomCmd(), "set", "cozy", "limits.tasksMax"); err != nil || strings.TrimSpace(shown) != "512" {
		t.Fatalf("shown tasks = %q, %v", shown, err)
	}
	if _, err := execCmd(t, RoomCmd(), "set", "cozy", "limits.memoryMax", "off"); err != nil {
		t.Fatal(err)
	}
	if l := stored(room.LimitMemoryMax); !l.Off {
		t.Fatal("off was not stored")
	}
	if _, err := execCmd(t, RoomCmd(), "set", "cozy", "limits.memoryHigh", "default"); err != nil {
		t.Fatal(err)
	}
	if l := stored(room.LimitMemoryHigh); l != (room.Limit{}) {
		t.Fatalf("default did not clear the limit: %+v", l)
	}
	if _, err := execCmd(t, RoomCmd(), "set", "cozy", "limits.memoryHigh", "huge"); err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("bad size err = %v", err)
	}
	if _, err := execCmd(t, RoomCmd(), "set", "cozy", "limits.swap", "1G"); err == nil || !strings.Contains(err.Error(), "unknown room limit") {
		t.Fatalf("unknown limit err = %v", err)
	}
}
