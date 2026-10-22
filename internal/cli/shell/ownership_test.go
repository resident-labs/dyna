package shell

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/mode"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

func TestStopChecksShellOwner(t *testing.T) {
	storetest.Isolate(t)
	for _, key := range []string{"CODEX_THREAD_ID", "CLAUDE_CODE_SESSION_ID"} {
		t.Setenv(key, "")
	}
	rec, err := room.DefaultStore().Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	parent := "ses_20261018-000001-aaaaaaaa"
	owner := "ses_20261018-000002-bbbbbbbb"
	sibling := "ses_20261018-000003-cccccccc"
	for _, meta := range []subagent.Meta{
		{ID: parent, Name: "parent", Room: rec.Name, RoomID: rec.ID},
		{ID: owner, Name: "owner", Room: rec.Name, RoomID: rec.ID, ParentSession: parent},
		{ID: sibling, Name: "sibling", Room: rec.Name, RoomID: rec.ID, ParentSession: parent},
	} {
		recordtest.Write(t, subagent.DefaultStore().Root, meta.ID, meta)
	}
	st := shells.DefaultStore()
	owned, err := st.Create(shells.StartOptions{Name: "owned", Owner: owner, Cmd: []string{"/bin/true"}})
	if err != nil {
		t.Fatal(err)
	}
	rootOwned, err := st.Create(shells.StartOptions{Name: "root-owned", Cmd: []string{"/bin/true"}})
	if err != nil {
		t.Fatal(err)
	}
	// Completed real shells exercise the CLI guard without timing process startup
	// or sending signals to another process during the permission checks.
	for _, id := range []string{owned.ID, rootOwned.ID} {
		if err := st.Supervise(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		// Supervise ran in this test process and has returned. Clear its
		// supervisor identity so Stop sees a fully retired shell.
		meta, err := st.ReadMeta(id)
		if err != nil {
			t.Fatal(err)
		}
		meta.Supervisor = 0
		exit, err := st.ReadExit(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Save(meta, exit); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, actor, shell, deniedOwner string
	}{
		{"sibling refused", sibling, owned.ID, owner},
		{"owner allowed", owner, owned.ID, ""},
		{"ancestor allowed", parent, owned.ID, ""},
		{"root allowed", "", owned.ID, ""},
		{"session cannot stop root shell", owner, rootOwned.ID, "room owner"},
		{"root can stop root shell", "", rootOwned.ID, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(mode.EnvMode, "")
			t.Setenv(mode.EnvSession, tc.actor)
			if tc.actor != "" {
				t.Setenv(mode.EnvMode, mode.Subagent)
			}
			cmd := NewCommand()
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&output)
			cmd.SetArgs([]string{"stop", tc.shell})
			err := cmd.Execute()
			if tc.deniedOwner != "" {
				if err == nil || !strings.Contains(err.Error(), tc.deniedOwner) {
					t.Fatalf("stop must name owner %q: %v", tc.deniedOwner, err)
				}
			} else if err != nil || !strings.Contains(output.String(), `"status":"exited"`) {
				t.Fatalf("authorized stop: %v, output %s", err, output.String())
			}
		})
	}
}
