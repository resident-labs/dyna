package mode

import "testing"

// The internal root has a room stamp but is not a subagent session.
func TestRootModeActsAsOwner(t *testing.T) {
	t.Setenv(EnvMode, Root)
	t.Setenv(EnvSession, "ses_20260101-120000-abcdef12")
	t.Setenv(EnvDepth, "")
	t.Setenv(EnvRoomID, "room_20260101-120000-abcdef12")
	t.Setenv(EnvRoom, "amber-otter-42")
	t.Setenv(EnvAddress, "")
	t.Setenv(EnvWorkerRun, "")

	if s, ok := CurrentSession(); ok {
		t.Fatalf("CurrentSession() = %#v, want root", s)
	}
	if InsideDyna() {
		t.Fatal("InsideDyna() = true for the internal root")
	}
	if !OwnerProcess() {
		t.Fatal("OwnerProcess() = false for the internal root")
	}
}

func TestOwnerProcessRejectsDynaStamps(t *testing.T) {
	for _, env := range [][2]string{{EnvMode, Subagent}, {EnvSession, "ses_x"}, {EnvRoomID, "room_x"}, {EnvWorkerRun, "run_x"}} {
		t.Run(env[0]+"="+env[1], func(t *testing.T) {
			for _, k := range StampKeys() {
				t.Setenv(k, "")
			}
			t.Setenv(env[0], env[1])
			if OwnerProcess() {
				t.Fatal("OwnerProcess() = true")
			}
		})
	}
	for _, k := range StampKeys() {
		t.Setenv(k, "")
	}
	if !OwnerProcess() {
		t.Fatal("OwnerProcess() = false for an unstamped CLI")
	}
}

func TestStampFromEnv(t *testing.T) {
	t.Setenv(EnvRoomID, "room-test")
	t.Setenv(EnvRoom, "test")
	t.Setenv(EnvAddress, "test:decoder")
	stamp, ok := StampFromEnv()
	if !ok || stamp.RoomID != "room-test" || stamp.Room != "test" || stamp.Address != "test:decoder" {
		t.Fatalf("stamp = %+v, %v", stamp, ok)
	}
}
