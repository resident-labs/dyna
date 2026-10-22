package subagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/mode"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/turnlock"
)

func TestWakePreservesLockHeldByNonRunner(t *testing.T) {
	storetest.Isolate(t)
	held := &heldTurnSpawner{}
	t.Cleanup(func() {
		if held.lock != nil {
			held.lock.Close()
		}
	})
	m := &Manager{Store: DefaultStore(), Profiles: func() (*profile.Store, error) {
		return &profile.Store{Profiles: []profile.Profile{mockProfile("mock")}}, nil
	}, Settings: settings.Defaults, Spawner: held}
	rec, err := m.rooms().Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(mode.EnvRoomID, rec.ID)
	t.Setenv(mode.EnvRoom, rec.Name)
	info := completedFixtureSession(t, &testEnv{t: t, m: m, ctx: context.Background(), room: rec})
	lock, err := m.Store.tryTurnLock(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	before, err := lock.Stat()
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := m.Store.Dir(info.ID)
	if err := writeGateState(dir, gateState{Pid: os.Getppid(), Turn: info.Turns}); err != nil {
		t.Fatal(err)
	}
	if alive, known := turnlock.RunnerAlive(dir, info.ID); alive || !known {
		t.Fatalf("runner alive = %v, known = %v", alive, known)
	}
	_, err = m.Wake(context.Background(), info.ID, WakeOptions{continuation: "follow up"})
	if !errors.Is(err, ErrBusy) {
		t.Errorf("Wake error = %v; want ErrBusy", err)
	}
	after, err := os.Stat(filepath.Join(dir, "turn.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Error("Wake replaced the held lock inode")
	}
	meta, err := m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Turns != info.Turns || held.lock != nil {
		t.Fatalf("turns = %d, spawned = %v", meta.Turns, held.lock != nil)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := m.Store.tryTurnLock(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
}
