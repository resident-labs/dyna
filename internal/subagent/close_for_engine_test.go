package subagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
)

// The checked close still refuses a caller the rules refuse, in the engine
// too. CloseForEngine checks no caller and records the engine as who closed
// the session; a checked close records its caller.
func TestCloseForEngineRecordsTheEngine(t *testing.T) {
	m, rooms, work, metas := actorFixture(t)
	m.Rooms = rooms
	t.Cleanup(authority.InEngine())
	worker := authority.WithActor(context.Background(), authority.Actor{Role: authority.Worker, Session: metas["lead"].ID, Room: work.ID})
	if _, err := m.CloseWithOptions(worker, metas["child"].ID, CloseOptions{Force: true}); err == nil {
		t.Fatal("a worker closed a session through the checked close")
	}
	if _, err := m.CloseForEngine(worker, metas["child"].ID, "test maintenance", CloseOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	owner := authority.WithActor(context.Background(), authority.Actor{Role: authority.Owner})
	if _, err := m.CloseWithOptions(owner, metas["other"].ID, CloseOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"child": "engine: test maintenance", "other": string(authority.Owner)} {
		if got := closedBy(t, m, metas[name].ID); got != want {
			t.Errorf("%s session_closed by = %q, want %q", name, got, want)
		}
	}
}

func closedBy(t *testing.T, m *Manager, id string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(m.Store.Root, id, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	by := ""
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var ev map[string]any
		if json.Unmarshal([]byte(line), &ev) == nil && ev["t"] == "session_closed" {
			by, _ = ev["by"].(string)
		}
	}
	return by
}

// A session may not ask for a service role: it would start a session with
// no parent. StartForEngine is the engine's way to start one.
func TestStartRefusesAServiceRoleFromASession(t *testing.T) {
	e := newTestEnv(t)
	lead, _ := e.start("RESPOND: ready")
	t.Cleanup(authority.InEngine())
	ctx := authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Session: lead.ID, Room: lead.RoomID})
	for _, role := range []string{RoleKeeper, RoleLibrarian, RoleSteward, RoleRoot, "reviewer"} {
		opts := StartOptions{Role: role, Goal: "Finish the test task", Task: "RESPOND: ok", Profile: "mock", Dir: t.TempDir()}
		if _, _, err := e.m.Start(ctx, opts); err == nil || !strings.Contains(err.Error(), "only the owner or root may start a service role") {
			t.Errorf("a session started role %q: %v", role, err)
		}
	}
}
