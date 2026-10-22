package workflows

import (
	"context"
	"dyna-agent/internal/workflowsvc"
	"strings"
	"testing"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
)

// `dyna runs steer` is deleted. Steering a workflow worker is a post that
// mentions @run:<id>/agent:<n>: it is stored in the run's room and queued into
// the worker's steering queue.
func TestPostToRunWorkerQueuesSteering(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_cli-steering")
	work, err := room.DefaultStore().Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DYNA_ROOM_ID", work.ID) // real runs are always stamped with a room
	t.Setenv("DYNA_ROOM", work.Name)
	t.Setenv(runstore.AgentJournalRootEnv, "")
	run, err := runstore.Create("cli steering", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Finish("ok", "null", nil)
	if _, err := run.StartAgentJournal(3, "worker", "test", "", "task"); err != nil {
		t.Fatal(err)
	}
	if err := runstore.ActivateAgentSteering(run.Meta.ID, 3); err != nil {
		t.Fatal(err)
	}

	svc := notify.New(nil, nil)
	owner := notify.Sender{Kind: notify.SenderOwner}
	addr := "run:" + run.Meta.ID + "/agent:3"
	res, err := svc.Post(context.Background(), notify.PostInput{Sender: owner, Mentions: []string{"@" + addr}, Text: "inspect the parser first"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deliveries) != 1 || res.Deliveries[0].Delivery != "worker-steered" || res.Message.To[0] != addr || res.Message.From != work.Name {
		t.Fatalf("post result = %#v", res)
	}
	messages, _, err := runstore.ReadAgentSteeringFrom(run.Meta.ID, 3, 0, false)
	if err != nil || len(messages) != 1 || !strings.Contains(messages[0].Message, "inspect the parser first") {
		t.Fatalf("steering messages = %#v, %v", messages, err)
	}
	if !strings.Contains(messages[0].Message, "[dyna message "+res.Message.ID+" from work]") || !strings.Contains(messages[0].Message, "To: "+addr+" (you)") {
		t.Fatalf("steering frame = %q", messages[0].Message)
	}
	stored, err := room.DefaultStore().Messages(run.Meta.RoomID)
	if err != nil || len(stored) != 1 || stored[0].ID != res.Message.ID {
		t.Fatalf("stored messages = %#v, %v", stored, err)
	}

	// Bad worker mentions are rejected before anything is stored.
	for _, bad := range []string{"@" + addr[:len(addr)-1] + "not-a-number", "@run:" + run.Meta.ID + "/agent:0", "@run:" + run.Meta.ID} {
		if _, err := svc.Post(context.Background(), notify.PostInput{Sender: owner, Mentions: []string{bad}, Text: "message"}); err == nil {
			t.Fatalf("mention %q accepted", bad)
		}
	}
	if stored, _ := room.DefaultStore().Messages(run.Meta.RoomID); len(stored) != 1 {
		t.Fatalf("rejected mentions stored posts: %#v", stored)
	}
}

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
