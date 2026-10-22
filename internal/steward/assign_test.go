package steward

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/subagent"
)

type fakeSessions struct {
	info                    subagent.Info
	turns                   []subagent.Turn
	starts, assigns, closes int
	startErr                error
}

func (f *fakeSessions) Profiles() ([]profile.Profile, error) {
	return []profile.Profile{{Name: "mock", Harness: profile.HarnessMock}}, nil
}
func (f *fakeSessions) Failures(time.Time) (map[string]time.Time, error) { return nil, nil }
func (f *fakeSessions) Inspect(string) (subagent.Info, subagent.Turn, error) {
	return f.info, f.turns[len(f.turns)-1], nil
}
func (f *fakeSessions) Turn(_ string, n int) (subagent.Turn, error) { return f.turns[n-1], nil }
func (f *fakeSessions) Named(string) (subagent.Info, error) {
	if f.info.ID == "" {
		return f.info, errors.New("missing")
	}
	return f.info, nil
}
func (f *fakeSessions) Start(roomID, prof, dir, goal, task string) (subagent.Info, int, error) {
	f.starts++
	if f.startErr != nil {
		return subagent.Info{}, 0, f.startErr
	}
	f.info = subagent.Info{Meta: subagent.Meta{ID: "steward", RoomID: roomID, Role: subagent.RoleSteward, Profile: prof, Status: subagent.StatusIdle, Turns: 1, Resumable: true, HarnessSession: "session"}}
	f.turns = []subagent.Turn{{N: 1, Prompt: task, Status: subagent.TurnOK}}
	return f.info, 1, nil
}
func (f *fakeSessions) Assign(_ context.Context, _ string, task string) (int, error) {
	f.assigns++
	f.info.Turns++
	f.turns = append(f.turns, subagent.Turn{N: f.info.Turns, Prompt: task, Status: subagent.TurnOK})
	return f.info.Turns, nil
}
func (f *fakeSessions) Close(context.Context, string) error { f.closes++; return nil }

func TestAssignStartsReusesAndRepairsRestart(t *testing.T) {
	f := &fakeSessions{}
	s := Service{Sessions: f, Root: t.TempDir()}
	for _, key := range []string{"batch1", "batch1", "batch2"} {
		if err := s.Assign(t.Context(), "room", t.TempDir(), "", key, "TestBroken failed"); err != nil {
			t.Fatal(err)
		}
	}
	if f.starts != 1 || f.assigns != 1 || f.closes != 0 {
		t.Fatalf("starts %d assigns %d closes %d", f.starts, f.assigns, f.closes)
	}
	if !strings.Contains(f.turns[0].Prompt, "TestBroken failed") || !strings.Contains(f.turns[0].Prompt, "Never resume while main is red") {
		t.Fatal("missing report or playbook")
	}
}

func TestAssignReturnsStartFailure(t *testing.T) {
	f := &fakeSessions{startErr: errors.New("provider failed")}
	s := Service{Sessions: f, Root: t.TempDir()}
	if err := s.Assign(t.Context(), "room", t.TempDir(), "", "batch1", "failure"); err == nil {
		t.Fatal("start failure hidden")
	}
}
