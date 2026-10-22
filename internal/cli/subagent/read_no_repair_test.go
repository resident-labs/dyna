package subagent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

// The CLI's read commands never repair a session: a read must not write
// (item 4), so a dead runner's turn stays for the engine's room watch to
// repair.
func TestReadCommandsDoNotRepairSessions(t *testing.T) {
	for _, args := range [][]string{
		{"list"},
		{"show", orphanID},
		{"show", orphanID, "--json"},
		{"output", orphanID, "--transcript"},
		{"history", orphanID},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			m := orphanSession(t)
			repairs := 0
			m.Store.ReportRepaired = func(*subagent.Store, string, int) { repairs++ }
			cmd := newCommand(func() *subagent.Manager { return m })
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(args)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("dyna subagent %v: %v\n%s", args, err, out.String())
			}
			if repairs != 0 {
				t.Fatalf("dyna subagent %v repaired the session", args)
			}
			if turn, err := m.Store.ReadTurn(orphanID, 1); err != nil || turn.Status != subagent.TurnRunning {
				t.Fatalf("turn 1 = %q, %v; want it still running", turn.Status, err)
			}
		})
	}
}

const orphanID = "ses_20261017-000002-bbbbbbbb"

// orphanSession stores a session whose turn 1 is running with no runner
// holding turn.lock, which a repairing read would mark failed.
func orphanSession(t *testing.T) *subagent.Manager {
	t.Helper()
	storetest.Isolate(t)
	m := subagent.NewManager()
	work := workRoom(t, m)
	meta := subagent.Meta{ID: orphanID, Name: "orphan", RoomID: work.ID, Room: work.Name, Dir: t.TempDir(), Depth: 1, Profile: "mock", Turns: 1, Created: time.Now()}
	dir, _ := m.Store.Dir(meta.ID)
	if err := os.MkdirAll(filepath.Join(dir, "turns"), 0o700); err != nil {
		t.Fatal(err)
	}
	recordtest.Write(t, m.Store.Root, meta.ID, meta)
	raw, _ := json.Marshal(subagent.Turn{N: 1, Status: subagent.TurnRunning, Prompt: "unfinished task", Started: time.Now()})
	if err := os.WriteFile(filepath.Join(dir, "turns", "1.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return m
}
