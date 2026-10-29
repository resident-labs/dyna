package configsvc

import (
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/subagent"
)

// The fan-out must wait for the reply. The test queues it, so a synchronous
// fan-out fails at once and no goroutine has to block.
func TestProfileSaveRepliesBeforeInstructionFanOut(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	profiles := &profile.Store{Path: profile.DefaultPath(), Profiles: []profile.Profile{
		{Name: "mock", Harness: profile.HarnessMock, Default: true, Taste: 5, Intelligence: 5, Cost: 5},
	}}
	if err := profiles.Save(); err != nil {
		t.Fatal(err)
	}
	m := subagent.NewManager()
	fanOuts := 0
	m.InstructionsChanged = func() { fanOuts++ }
	var queued []func()
	defer func(run func(func())) { afterReply = run }(afterReply)
	afterReply = func(fn func()) { queued = append(queued, fn) }
	svc := &Service{Sessions: m}
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	if err := svc.UpsertProfile(ctx, profile.Profile{Name: "writer", Harness: profile.HarnessMock, Model: "model-2", Taste: 7, Intelligence: 8, Cost: 6}); err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 || fanOuts != 0 {
		t.Fatalf("reply waited for the fan-out: queued %d, fan-outs %d", len(queued), fanOuts)
	}
	queued[0]()
	if fanOuts != 1 {
		t.Fatalf("queued fan-out ran %d times, want 1", fanOuts)
	}
}
