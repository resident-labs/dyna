package subagent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

// start reads the brief and the schema itself, resolves the room with its
// own stamp, and asks the engine to start the session with its directory;
// the CLI process creates no session.
func TestStartCallsTheEngine(t *testing.T) {
	storetest.Isolate(t)
	m := subagent.NewManager()
	work := workRoom(t, m)
	brief := t.TempDir() + "/brief.md"
	if err := os.WriteFile(brief, []byte("Do the task."), 0o600); err != nil {
		t.Fatal(err)
	}
	var room string
	var got roomsvc.SubagentStart
	enginetest.Serve(t, store.DataDir(), map[string]websvc.Call{"StartSubagent": func(_ context.Context, args []json.RawMessage) (any, error) {
		if err := json.Unmarshal(args[0], &room); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(args[1], &got); err != nil {
			return nil, err
		}
		return subagent.Info{Meta: subagent.Meta{ID: "ses_20261017-000001-aaaaaaaa", Name: "kid", Room: work.Name, RoomID: work.ID}}, nil
	}})
	cwd := t.TempDir()
	t.Chdir(cwd)
	runCLI(t, m, "start", "--room", work.Name, "--goal", "Finish the test task", "--profile", "mock", "--name", "kid", "@"+brief)
	if room != work.ID || got.Task != "Do the task." || got.Goal != "Finish the test task" || got.Profile != "mock" || got.Name != "kid" || got.Cwd != cwd || got.Dir != "" {
		t.Fatalf("StartSubagent got room %q and %+v; want room %s, the brief text and cwd %s", room, got, work.ID, cwd)
	}
	if sessions, err := m.Store.ListSnapshot(true); err != nil || len(sessions) != 0 {
		t.Fatalf("the CLI created sessions itself: %v, %v", sessions, err)
	}
}

// Keep the CLI error path covered without another CLI process or a live
// harness in the lifecycle e2e test. The engine uses its real name check.
func TestStartPreservesEngineDuplicateNameError(t *testing.T) {
	storetest.Isolate(t)
	m := subagent.NewManager()
	m.Profiles = func() (*profile.Store, error) {
		return &profile.Store{Profiles: []profile.Profile{{Name: "mock", Harness: profile.HarnessMock,
			Taste: 5, Intelligence: 5, Cost: 5, Default: true}}}, nil
	}
	m.Executable = t.TempDir() + "/must-not-start"
	work := workRoom(t, m)
	existing := subagent.Meta{ID: "ses_20261021-022400-aaaaaaaa", Name: "kid",
		Room: work.Name, RoomID: work.ID, Dir: t.TempDir(), Depth: 1, Created: time.Now()}
	recordtest.Write(t, m.Store.Root, existing.ID, existing)
	serveSessionControls(t, m)

	cmd := newCommand(func() *subagent.Manager { return m })
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"start", "--room", work.Name, "--goal", "Finish the test task",
		"--profile", "mock", "--name", existing.Name, "Do the work."})
	err := cmd.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), `already named "kid"`) || !strings.Contains(err.Error(), "pick another name") {
		t.Fatalf("duplicate name error = %v; want the engine name-conflict message", err)
	}
	if strings.Contains(stdout.String(), existing.Address()) || strings.Contains(stderr.String(), "running in background") {
		t.Fatalf("rejected start printed success: stdout %q, stderr %q", stdout.String(), stderr.String())
	}
	if sessions, err := m.Store.ListSnapshot(true); err != nil || len(sessions) != 1 || sessions[0].ID != existing.ID {
		t.Fatalf("rejected start changed sessions: %+v, %v", sessions, err)
	}
}
