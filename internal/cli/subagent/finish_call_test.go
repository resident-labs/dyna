package subagent

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/mode"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

// finish asks the engine for the named session, or for the caller's own
// session by default; the CLI process makes no session dormant itself.
func TestFinishCallsTheEngine(t *testing.T) {
	storetest.Isolate(t)
	m := subagent.NewManager()
	work := room.Record{ID: "room_finish_fixture", Name: "work"}
	lead := subagent.Meta{ID: "ses_20261017-000001-aaaaaaaa", Name: "lead", Room: work.Name, RoomID: work.ID, Dir: ".", Depth: 1, Created: time.Now(), Status: subagent.StatusIdle}
	child := lead
	child.ID, child.Name, child.Depth, child.ParentSession = "ses_20261017-000002-bbbbbbbb", "child", 2, lead.ID
	for _, meta := range []subagent.Meta{lead, child} {
		recordtest.Write(t, m.Store.Root, meta.ID, meta)
	}
	t.Setenv(mode.EnvMode, mode.Subagent)
	t.Setenv(mode.EnvSession, lead.ID)
	t.Setenv(mode.EnvRoomID, work.ID)
	t.Setenv(mode.EnvRoom, work.Name)
	got := serveRecordedCalls(t, "FinishSession")
	cmd := finishCommand(func() *subagent.Manager { return m })
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	for _, args := range [][]string{{}, {child.ID}} {
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("finish %v: %v: %s", args, err, out.String())
		}
	}
	wantCalls(t, got, map[string][]string{"FinishSession": {work.ID, lead.ID, work.ID, child.ID}})
	for _, expected := range []string{
		"finished work:lead (" + lead.ID + "); dormant until a message wakes it",
		"finished work:child (" + child.ID + "); dormant until a message wakes it",
	} {
		if !strings.Contains(out.String(), expected) {
			t.Errorf("finish output = %q, want %q", out.String(), expected)
		}
	}
	for _, id := range []string{lead.ID, child.ID} {
		if meta, err := m.Store.ReadMeta(id); err != nil || meta.Status == subagent.StatusDormant {
			t.Fatalf("the CLI finished %s itself: %+v, %v", id, meta.Status, err)
		}
	}
}
