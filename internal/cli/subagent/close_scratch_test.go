package subagent

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/scratch"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

// Closing an already closed session again must report the deadline from its
// stored close time, not seven days from today.
func TestRepeatedCloseReportsScratchDeadlineFromStoredCloseTime(t *testing.T) {
	storetest.Isolate(t)
	t.Setenv(scratch.RootEnv, filepath.Join(t.TempDir(), "scratch"))
	m := subagent.NewManager()
	work := workRoom(t, m)
	closedAt := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	meta := subagent.Meta{ID: "ses_20261017-000001-aaaaaaaa", Name: "done", Room: work.Name, RoomID: work.ID, Dir: t.TempDir(), Depth: 1, Created: closedAt.Add(-time.Hour), Closed: closedAt}
	recordtest.Write(t, m.Store.Root, meta.ID, meta)
	path, err := scratch.Ensure(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	serveSessionControls(t, m)
	cmd := newCommand(func() *subagent.Manager { return m })
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"close", meta.ID})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("close: %v: %s", err, out.String())
	}
	if want := "scratch kept at " + path + " until 2026-10-27"; !strings.Contains(out.String(), want) {
		t.Fatalf("close output = %q, want %q", out.String(), want)
	}
}
