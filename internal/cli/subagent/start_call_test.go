package subagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/runstore"
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
	var item string
	enginetest.Serve(t, store.DataDir(), map[string]websvc.Call{"StartSubagent": func(_ context.Context, args []json.RawMessage) (any, error) {
		if err := json.Unmarshal(args[0], &room); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(args[1], &got); err != nil {
			return nil, err
		}
		return subagent.Info{Meta: subagent.Meta{ID: "ses_20261017-000001-aaaaaaaa", Name: "kid", Room: work.Name, RoomID: work.ID, Goal: "g8", Item: item}}, nil
	}})
	cwd := t.TempDir()
	t.Chdir(cwd)
	for _, item = range []string{"i3", ""} {
		for _, asJSON := range []bool{false, true} {
			cmd := newCommand(func() *subagent.Manager { return m })
			var stdout, stderr bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)
			args := []string{"start", "--room", work.Name, "--goal", "Finish the test task", "--profile", "mock", "--name", "kid", "--fixes", "D-35,D-40", "@" + brief}
			if asJSON {
				args = append(args, "--json")
			}
			cmd.SetArgs(args)
			if err := cmd.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if asJSON {
				var out map[string]any
				if err := json.Unmarshal(stdout.Bytes(), &out); err != nil || out["goal"] != "g8" || item != "" && out["item"] != item || item == "" && out["item"] != nil {
					t.Fatalf("start JSON = %s, %v", stdout.String(), err)
				}
			} else {
				want := "holds goal g8"
				if item != "" {
					want = "assigned item i3 on goal g8"
				}
				if !strings.Contains(stderr.String(), want) {
					t.Fatalf("start status = %q; want %q", stderr.String(), want)
				}
			}
		}
	}
	if !runstore.IsSessionDirID(got.ID) {
		t.Fatalf("StartSubagent got session ID %q, want the caller's ID for a late answer", got.ID)
	}
	if room != work.ID || got.Task != "Do the task." || got.Goal != "Finish the test task" || got.Profile != "mock" || got.Name != "kid" || got.Cwd != cwd || got.Dir != "" || len(got.Fixes) != 2 || got.Fixes[0] != 35 || got.Fixes[1] != 40 {
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

// A StartSubagent answer that arrives late still reports the session the
// engine created, so a caller does not start a duplicate.
func TestLateStartReportsTheCreatedSession(t *testing.T) {
	storetest.Isolate(t)
	m := subagent.NewManager()
	work := workRoom(t, m)
	id := subagent.NewID()
	recordtest.Write(t, m.Store.Root, id, subagent.Meta{ID: id, Name: "kid", Room: work.Name, RoomID: work.ID, Goal: "g8", Created: time.Now()})
	var stderr bytes.Buffer
	info, ok := lateStart(m, id, callspec.Late("StartSubagent", 5*time.Second), &stderr)
	if !ok || info.ID != id || info.Address() != work.Name+":kid" {
		t.Fatalf("late start = %+v, %v; want the created session", info, ok)
	}
	if !strings.Contains(stderr.String(), "answered StartSubagent late") {
		t.Fatalf("notice = %q", stderr.String())
	}
	// Any other failure is not a late answer.
	if _, ok := lateStart(m, id, errors.New("socket closed"), &stderr); ok {
		t.Fatal("a plain failure was reported as a late start")
	}
}
