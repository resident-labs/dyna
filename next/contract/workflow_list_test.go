package contract

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestWorkflowListWireGolden(t *testing.T) {
	run := WorkflowInfo{Run: "wf", Room: "room", Session: "crew", Name: "sweep", Profiles: []ProfileName{"p"}, State: WorkflowDone, Started: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	got, err := json.Marshal(WorkflowListResponse{Runs: []WorkflowInfo{run}})
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/workflow-list.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != strings.TrimSpace(string(want)) {
		t.Fatalf("list wire shape: %s", got)
	}
	run.Result = "the script's result"
	status, err := json.Marshal(WorkflowStatusResponse{Run: run, Steps: []WorkflowStep{}})
	if err != nil || !strings.Contains(string(status), `"result":"the script's result"`) {
		t.Fatalf("status result: %s, %v", status, err)
	}
}
