package subagent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

// In `starting`, the window that applies is the startup deadline, not the
// handoff deadline that already passed; a normal start must not look
// overdue (D-434).
func TestTransferStatusShowsTheWindowThatApplies(t *testing.T) {
	storetest.Isolate(t)
	p := profile.Profile{Name: "next", Harness: profile.HarnessMock, Model: "default-model", Taste: 5, Intelligence: 5, Cost: 5}
	m := subagent.NewManager()
	work := workRoom(t, m)
	m.Profiles = func() (*profile.Store, error) { return &profile.Store{Profiles: []profile.Profile{p}}, nil }
	m.TransferLaunch = func(*subagent.Manager, string, string) error { return nil }
	meta := subagent.Meta{ID: "ses_20261017-000002-aaaaaaaa", Name: "worker", RoomID: work.ID, Room: work.Name, Dir: t.TempDir(), Depth: 1, Profile: "next", Model: "default-model", Turns: 2, Created: time.Now()}
	dir, _ := m.Store.Dir(meta.ID)
	os.MkdirAll(filepath.Join(dir, "turns"), 0700)
	recordtest.Write(t, m.Store.Root, meta.ID, meta)
	serveSessionControls(t, m)
	call := func(args ...string) string {
		t.Helper()
		cmd := newCommand(func() *subagent.Manager { return m })
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	now := time.Date(2026, 10, 28, 23, 16, 0, 0, time.Local)
	meta.Transfer = &subagent.TransferState{
		ID: "ses_20261028-231420-902e3d39", Session: meta.ID, Phase: "starting",
		Profile: "next", Model: "default-model", PreviousProfile: "old",
		Requested: now.Add(-90 * time.Second), Deadline: now.Add(-60 * time.Second),
		StartupDeadline: now.Add(3 * time.Minute), Turn: 3, CompactionExtended: true,
	}
	recordtest.Write(t, m.Store.Root, meta.ID, meta)
	out := call("transfer", "status", meta.ID)
	if !strings.Contains(out, "starting") || !strings.Contains(out, "Startup deadline: "+meta.Transfer.StartupDeadline.Format(time.RFC3339)) {
		t.Fatalf("starting status lacks its startup window: %s", out)
	}
	if !strings.Contains(out, "waits for the new model's first output") || !strings.Contains(out, "extended once for compaction") {
		t.Fatalf("starting status does not say what it waits for: %s", out)
	}
	if strings.Contains(out, "Deadline: "+meta.Transfer.Deadline.Format(time.RFC3339)) {
		t.Fatalf("a starting transfer still shows its passed handoff deadline: %s", out)
	}
	// The handoff window stays the one shown while the old model can answer.
	meta.Transfer.Phase, meta.Transfer.StartupDeadline, meta.Transfer.CompactionExtended = "waiting", time.Time{}, false
	meta.Transfer.Deadline = now.Add(30 * time.Second)
	recordtest.Write(t, m.Store.Root, meta.ID, meta)
	out = call("transfer", "status", meta.ID)
	if !strings.Contains(out, "Deadline: "+meta.Transfer.Deadline.Format(time.RFC3339)) {
		t.Fatalf("waiting status lost its handoff deadline: %s", out)
	}
	var state subagent.TransferState
	if err := json.Unmarshal([]byte(call("transfer", "status", meta.ID, "--json")), &state); err != nil || state.Phase != "waiting" {
		t.Fatalf("json status: %v (%s)", err, state.Phase)
	}
}
