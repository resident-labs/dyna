package goalkeeper

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/subagent"
)

type fakeSessions struct {
	info                             subagent.Info
	turn                             subagent.Turn
	starts, wakes, transfers, closes int
	wait                             func(context.Context) error
	result                           any
	lifecycle                        []string
	profiles                         []profile.Profile
}

func (f *fakeSessions) Profiles() ([]profile.Profile, error) {
	if f.profiles != nil {
		return f.profiles, nil
	}
	return []profile.Profile{{Name: "best", Harness: profile.HarnessMock, Taste: 8, Cost: 9}, {Name: "backup", Harness: profile.HarnessMock, Taste: 6, Cost: 7}}, nil
}
func (f *fakeSessions) Inspect(string) (subagent.Info, subagent.Turn, error) {
	if f.info.ID == "" {
		return f.info, f.turn, fmt.Errorf("missing")
	}
	return f.info, f.turn, nil
}
func (f *fakeSessions) Named(string) (subagent.Info, error) {
	if f.info.ID == "" {
		return f.info, fmt.Errorf("missing")
	}
	return f.info, nil
}
func (f *fakeSessions) Start(ctx context.Context, roomID, name, task, schema string, deadline time.Time) (subagent.Info, subagent.Turn, error) {
	f.starts++
	f.lifecycle = append(f.lifecycle, "start")
	f.info = subagent.Info{Meta: subagent.Meta{ID: "keeper-session", Role: "keeper", Name: "keeper", RoomID: roomID, Profile: name, Turns: 1, Resumable: true, HarnessSession: "h"}, Status: subagent.StatusRunning}
	f.turn = subagent.Turn{N: 1, Status: subagent.TurnRunning, Prompt: task}
	return f.info, f.turn, nil
}
func (f *fakeSessions) Continue(ctx context.Context, id, task, schema string, deadline time.Time) (subagent.Info, subagent.Turn, error) {
	f.wakes++
	f.lifecycle = append(f.lifecycle, "continue")
	f.info.Turns++
	f.info.Status = subagent.StatusRunning
	f.turn = subagent.Turn{N: f.info.Turns, Status: subagent.TurnRunning, Prompt: task}
	return f.info, f.turn, nil
}
func (f *fakeSessions) Wait(ctx context.Context, id string, n int) (subagent.Info, subagent.Turn, error) {
	if f.wait != nil {
		if err := f.wait(ctx); err != nil {
			return f.info, f.turn, err
		}
	}
	f.info.Status = subagent.StatusIdle
	f.turn.Status = subagent.TurnOK
	f.turn.Result = f.result
	return f.info, f.turn, nil
}
func (f *fakeSessions) Close(context.Context, string) error {
	f.closes++
	f.lifecycle = append(f.lifecycle, "close")
	f.info.ID = ""
	return nil
}
func (f *fakeSessions) Transfer(ctx context.Context, id, name string) error {
	f.transfers++
	f.info.Profile = name
	f.turn.Status = subagent.TurnOK
	return nil
}
func (f *fakeSessions) Failures(time.Time) (map[string]time.Time, error) { return nil, nil }
func TestTickWithoutPassesStartsNoKeeperSession(t *testing.T) {
	e := newCheckInEnv(t)
	e.f.turn = subagent.Turn{N: 1, Status: subagent.TurnError, Error: "provider quota exceeded", Ended: e.now}
	if _, err := e.s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.f.starts != 0 || e.f.closes != 0 {
		t.Fatalf("a session turned over with no pass to run: starts=%d closes=%d", e.f.starts, e.f.closes)
	}
}

func TestKeeperProfileUsesSharedHarnessPolicy(t *testing.T) {
	for _, pin := range []string{"", "unsafe"} {
		t.Run(pin, func(t *testing.T) {
			e := newCheckInEnv(t)
			e.f.info = subagent.Info{}
			e.f.profiles = []profile.Profile{{Name: "unsafe", Harness: profile.HarnessCustom, Taste: 100, Cost: 100}, {Name: "codex", Harness: profile.HarnessCodex, Taste: 8, Cost: 9}, {Name: "claude", Harness: profile.HarnessClaudeCode, Taste: 6, Cost: 7}}
			selected, _, err := e.s.KeeperProfile(e.rec.ID, pin)
			e.v.GoalKeeperProfile = pin
			if pin == "unsafe" {
				if err == nil || !strings.Contains(err.Error(), "custom") {
					t.Fatalf("an explicit unsupported profile fell back: err=%v", err)
				}
			} else if err != nil || selected != "codex" {
				t.Fatalf("auto chose an unsupported profile: selected=%s err=%v", selected, err)
			}
		})
	}
}
