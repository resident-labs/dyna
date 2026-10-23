package goalkeeper

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

func TestLiveKeeperCreatesServiceSession(t *testing.T) {
	root := storetest.Isolate(t)
	rooms := &room.Store{Root: filepath.Join(root, "rooms")}
	rec, err := rooms.Create(room.CreateInput{Name: "services"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var runners sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		runners.Wait()
	})
	manager := &subagent.Manager{Store: &subagent.Store{Root: filepath.Join(root, "sessions")}, Profiles: func() (*profile.Store, error) {
		return &profile.Store{Profiles: []profile.Profile{{Name: "mock", Harness: profile.HarnessMock}}}, nil
	}, Spawner: subagent.InProcessSpawner{Ctx: ctx, Runners: &runners}}
	service := &Service{Store: &goals.Store{Root: filepath.Join(root, "goals")}, Notify: notify.New(rooms, manager)}
	backend := liveSessions{service: service}
	info, _, err := backend.Start(ctx, rec.ID, "mock", "SLEEP:0\nRESPOND: {\"entries\":[]}", `{"type":"object"}`, time.Now().Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !info.Service || info.Role != "keeper" {
		t.Fatalf("keeper metadata: %+v", info.Meta)
	}
	// Join the runner without sharing the keeper turn deadline.
	runners.Wait()
	turn, err := manager.Store.ReadTurn(info.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if turn.Status != subagent.TurnOK {
		t.Fatalf("keeper turn: %+v", turn)
	}
}
