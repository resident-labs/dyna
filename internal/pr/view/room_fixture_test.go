package view_test

import (
	"context"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
	"testing"
	"time"
)

type env struct {
	t   testing.TB
	svc *roomsvc.Service
	mgr *subagent.Manager
	ctx context.Context
}

func newEnv(t testing.TB) *env {
	t.Helper()
	storetest.Isolate(t)
	t.Setenv(runstore.AgentJournalRootEnv, "")
	profiles := &profile.Store{Profiles: []profile.Profile{{Name: "mock", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5, Default: true}}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr := &subagent.Manager{
		Store:    subagent.DefaultStore(),
		Profiles: func() (*profile.Store, error) { return profiles, nil },
		Settings: settings.Defaults,
		Spawner:  subagent.InProcessSpawner{Ctx: ctx},
	}
	return &env{t: t, svc: roomsvc.New(notify.New(room.DefaultStore(), mgr)), mgr: mgr, ctx: ctx}
}

func (e *env) room(name string) room.Record {
	e.t.Helper()
	rec, err := e.svc.Notify.Rooms.Create(room.CreateInput{Name: name, CreatedBy: "test"})
	if err != nil {
		e.t.Fatal(err)
	}
	return rec
}

func (e *env) as(parent *subagent.Info) {
	storetest.ClearStamp(e.t)
	if parent != nil {
		e.t.Setenv(mode.EnvMode, mode.Subagent)
		e.t.Setenv(mode.EnvSession, parent.ID)
		e.t.Setenv(mode.EnvDepth, "1")
		e.t.Setenv(mode.EnvRoom, parent.Room)
		e.t.Setenv(mode.EnvRoomID, parent.RoomID)
	}
}

func (e *env) start(rec room.Record, name string, parent *subagent.Info) subagent.Info {
	e.t.Helper()
	e.as(parent)
	defer e.as(nil)
	opts := subagent.StartOptions{Goal: "Finish the test task", Profile: "mock", Name: name, Dir: e.t.TempDir(), Task: "SLEEP:0\nRESPOND: ready"}
	if parent == nil {
		opts.Room = rec.Name
	}
	info, _, err := e.mgr.Start(context.Background(), opts)
	if err != nil {
		e.t.Fatalf("Start %s: %v", name, err)
	}
	return e.wait(info.ID, 1)
}

func (e *env) wait(id string, turn int) subagent.Info {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(e.ctx, 20*time.Second)
	defer cancel()
	info, _, err := e.mgr.Wait(ctx, id, turn)
	if err != nil {
		e.t.Fatalf("Wait(%s, %d): %v", id, turn, err)
	}
	return info
}
