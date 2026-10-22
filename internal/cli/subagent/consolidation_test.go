package subagent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/mode"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

func TestConsolidateHelpExplainsDurableRecovery(t *testing.T) {
	cmd := consolidationCommand(func() *subagent.Manager { return nil })
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"30-second", "worktrees", "--keep", "--profile", "resume", "cancel"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing help %s", want)
		}
	}
}

func TestConsolidateCLIRequestStatusRecoveryAndCancel(t *testing.T) {
	storetest.Isolate(t)
	p := profile.Profile{Name: "mock", Harness: profile.HarnessMock, Model: "kept-model", Taste: 5, Intelligence: 5, Cost: 5}
	mgr := subagent.NewManager()
	mgr.Profiles = func() (*profile.Store, error) { return &profile.Store{Profiles: []profile.Profile{p}}, nil }
	mgr.ConsolidationLaunch = func(*subagent.Manager, string, string) error { return nil }
	work := workRoom(t, mgr)
	var err error
	ids := []string{"ses_20261017-000001-aaaaaaaa", "ses_20261017-000002-bbbbbbbb", "ses_20261017-000003-cccccccc"}
	outsider := ids[2]
	for i, id := range ids {
		meta := subagent.Meta{ID: id, Name: fmt.Sprintf("source%d", i), RoomID: work.ID, Room: work.Name, Depth: 1, Dir: t.TempDir(), Profile: "mock", Model: "kept-model", Created: time.Now(), Resumable: true}
		recordtest.Write(t, mgr.Store.Root, id, meta)
	}
	serveSessionControls(t, mgr)
	call := func(args ...string) (string, error) {
		cmd := newCommand(func() *subagent.Manager { return mgr })
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	raw, err := call("consolidate", ids[0], ids[1], "--keep", ids[0], "--json")
	if err != nil {
		t.Fatal(err)
	}
	var saved subagent.ConsolidationState
	if err = json.Unmarshal([]byte(raw), &saved); err != nil || saved.Model != "kept-model" {
		t.Fatalf("request JSON: %s %v", raw, err)
	}
	// An unrelated stamped session may read the operation but not change it.
	t.Setenv(mode.EnvMode, mode.Subagent)
	t.Setenv(mode.EnvSession, outsider)
	t.Setenv(mode.EnvRoomID, work.ID)
	if raw, err = call("consolidate", "status", saved.ID, "--json"); err != nil || !strings.Contains(raw, saved.ID) {
		t.Fatalf("outsider status: %s %v", raw, err)
	}
	for _, action := range []string{"resume", "cancel"} {
		if _, err = call("consolidate", action, saved.ID, "--json"); err == nil || !strings.Contains(err.Error(), "descendants") {
			t.Fatalf("outsider %s: %v", action, err)
		}
	}
	storetest.ClearStamp(t)
	for _, action := range []string{"status", "resume", "cancel"} {
		raw, err = call("consolidate", action, saved.ID, "--room", work.ID, "--json")
		if err != nil || !strings.Contains(raw, saved.ID) {
			t.Fatalf("%s: %s %v", action, raw, err)
		}
	}
}

// workRoom creates the room "work" in the manager's room store.
func workRoom(t *testing.T, m *subagent.Manager) room.Record {
	t.Helper()
	rec, err := m.RoomStore().Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	return rec
}
