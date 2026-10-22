package subagent

import (
	"bytes"
	"strings"
	"testing"

	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

func TestStartRequiresGoalAndBriefBeforeCreatingSession(t *testing.T) {
	storetest.Isolate(t)
	m := subagent.NewManager()
	work := workRoom(t, m)
	serveSessionControls(t, m)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"start", "--room", work.Name, "Do the work."}, "--goal is required"},
		{[]string{"start", "--room", work.Name, "--goal", "Ship it; done when merged"}, "a brief is required"},
		{[]string{"start", "--room", work.Name, "--goal", "Ship it; done when merged", "  "}, "a brief is required"},
	} {
		cmd := newCommand(func() *subagent.Manager { return m })
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(tc.args)
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%q: err = %v, want %q", tc.args, err, tc.want)
		}
	}
	if sessions, _ := m.Store.List(true); len(sessions) != 0 {
		t.Fatalf("rejected starts created sessions: %+v", sessions)
	}
}
