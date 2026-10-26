package workflows

import (
	"context"
	"dyna-agent/internal/workflowsvc"
	"strings"
	"testing"

	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
)

// A workflow script posts as run:<id>; @parent of a top-level run is the
// room owner.
func TestScriptPosterPostsAsRun(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_script-post")
	work, err := room.DefaultStore().Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DYNA_ROOM_ID", work.ID)
	t.Setenv("DYNA_ROOM", work.Name)
	run, err := runstore.Create("script post", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Finish("ok", "null", nil)
	res, err := workflowsvc.ScriptPoster(context.Background(), run.Meta.ID, []string{"@parent"}, "brief ready")
	if err != nil || res.ID == "" || len(res.Deliveries) != 1 || res.Deliveries[0]["to"] != work.Name {
		t.Fatalf("scriptPoster = %#v, %v", res, err)
	}
	stored, _ := room.DefaultStore().Messages(work.ID)
	if len(stored) != 1 || stored[0].From != "run:"+run.Meta.ID {
		t.Fatalf("stored = %#v", stored)
	}
	if _, err := workflowsvc.ScriptPoster(context.Background(), run.Meta.ID, []string{"@siblings"}, "x"); err == nil {
		t.Fatal("a script used @siblings")
	}
}

func TestRunsSteerSubcommandIsGone(t *testing.T) {
	cmd := NewRunsCommand()
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"steer", "wf_x", "1", "message"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("runs steer error = %v, want unknown command", err)
	}
	cmd = NewRunsCommand()
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"wait", "wf_x"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("runs wait error = %v, want unknown command", err)
	}
}
