package subagent

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

func TestFinishedCLIListsDismissesAndReopensExactHistory(t *testing.T) {
	storetest.Isolate(t)
	m := subagent.NewManager()
	work := workRoom(t, m)
	const id = "ses_20261017-000001-aaaaaaaa"
	meta := subagent.Meta{ID: id, Name: "done-session", RoomID: work.ID, Room: work.Name, Depth: 1, Dir: t.TempDir(), Created: time.Now(), Finished: time.Now(), Resumable: true, HarnessSession: "native-session", Profile: "mock"}
	recordtest.Write(t, m.Store.Root, id, meta)
	serveSessionControls(t, m)
	call := func(args ...string) string {
		t.Helper()
		cmd := newCommand(func() *subagent.Manager { return m })
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out.String()
	}
	if out := call("list"); !strings.Contains(out, "done-session") || !strings.Contains(out, "dormant") {
		t.Fatalf("missing finished session: %s", out)
	}
	call("close", meta.Address())
	if out := call("list"); strings.Contains(out, "done-session") {
		t.Fatalf("dismissed still visible: %s", out)
	}
	if out := call("list", "--all"); !strings.Contains(out, "done-session") {
		t.Fatalf("history lost: %s", out)
	}
	if out := call("reopen", meta.Address()); !strings.Contains(out, "reopened work:done-session") {
		t.Fatalf("reopen: %s", out)
	}
	info, err := m.Store.Info(id)
	if err != nil || info.Status != subagent.StatusIdle || info.HarnessSession != meta.HarnessSession || !info.Finished.IsZero() {
		t.Fatalf("reopen changed history: %+v %v", info, err)
	}
}
