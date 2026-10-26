package subagent

import (
	"context"
	"testing"
	"time"

	"dyna-agent/internal/harness"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store/storetest"
)

// BenchmarkRunAgentStart times a run agent's start call: the session record is
// created and its first turn begins. A real harness adds its own process
// start, which is the same for a session and for the old in-process call.
func BenchmarkRunAgentStart(b *testing.B) {
	defer func(previous time.Duration) { harness.MockDefaultDelay = previous }(harness.MockDefaultDelay)
	harness.MockDefaultDelay = 0
	storetest.Isolate(b)
	ctx, cancel := context.WithCancel(context.Background())
	workers := NewWorkers(ctx, nil)
	b.Cleanup(func() {
		cancel()
		workers.Wait(time.Second)
	})
	store := &profile.Store{Profiles: []profile.Profile{mockProfile("mock")}}
	m := &Manager{
		Store:    DefaultStore(),
		Profiles: func() (*profile.Store, error) { return store, nil },
		Settings: settings.Defaults,
		Spawner:  InProcessSpawner{Ctx: ctx},
		Workers:  workers,
	}
	rec, err := m.rooms().Create(room.CreateInput{Name: "work"})
	if err != nil {
		b.Fatal(err)
	}
	b.Setenv(mode.EnvRoomID, rec.ID)
	b.Setenv(mode.EnvRoom, rec.Name)
	dir := b.TempDir()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		opts := StartOptions{Run: &RunOwner{ID: "wf_bench", Agent: i + 1}, Profile: "mock", Task: "RESPOND: done", Dir: dir, Room: rec.ID}
		if _, err := m.StartRunAgent(ctx, opts); err != nil {
			b.Fatal(err)
		}
		// The wait polls every 150 ms; it is the result delivery, not the start.
		b.StopTimer()
		got, err := m.WaitRunAgent(ctx, "wf_bench", i+1, time.Minute)
		if err != nil || !got.Done {
			b.Fatalf("wait = %+v, %v", got, err)
		}
		b.StartTimer()
	}
}

// BenchmarkInProcessAgentStart is the old path on the same mock harness: the
// coordinator runs the harness itself, with no session. It includes the whole
// mock run, so it bounds the old start from above.
func BenchmarkInProcessAgentStart(b *testing.B) {
	defer func(previous time.Duration) { harness.MockDefaultDelay = previous }(harness.MockDefaultDelay)
	harness.MockDefaultDelay = 0
	prof := mockProfile("mock")
	dir := b.TempDir()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := harness.RunWithOptions(context.Background(), prof, "RESPOND: done", dir, true,
			harness.JournalOptions{}, harness.SteeringOptions{}, harness.SessionOptions{}, harness.ExecutionOptions{DisableNativeSubagents: true}); err != nil {
			b.Fatal(err)
		}
	}
}
