package subagent

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/mode"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

func TestSelfClosePointsToFinishAndFinishMakesCallerDormant(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(mode.EnvMode, mode.Subagent)
	const id = "ses_20261017-000001-aaaaaaaa"
	t.Setenv(mode.EnvSession, id)
	m := subagent.NewManager()
	work := workRoom(t, m)
	t.Setenv(mode.EnvRoomID, work.ID)
	t.Setenv(mode.EnvRoom, work.Name)
	meta := subagent.Meta{ID: id, Name: "finisher", Room: work.Name, RoomID: work.ID, Dir: t.TempDir(), Created: time.Now(), Depth: 2, ParentSession: "ses_20261017-000000-bbbbbbbb"}
	recordtest.Write(t, m.Store.Root, id, meta)
	serveSessionControls(t, m)
	call := func(args ...string) (string, error) {
		cmd := newCommand(func() *subagent.Manager { return m })
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	for _, ref := range []string{id, "work:finisher"} {
		if _, err := call("close", ref, "--force"); err == nil || !strings.Contains(err.Error(), "dyna subagent finish") {
			t.Fatalf("self close %s: %v", ref, err)
		}
	}
	if out, err := call("finish"); err != nil || !strings.Contains(out, "finished work:finisher") {
		t.Fatalf("finish caller: %q %v", out, err)
	}
	if out, err := call("show", id); err != nil || !strings.Contains(out, "dormant") || !strings.Contains(out, "dormant until a coordination post wakes it") {
		t.Fatalf("dormant display: %q %v", out, err)
	}
}
